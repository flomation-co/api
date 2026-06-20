package http

// Tests for the parent-execution-header path on POST
// /api/v1/flo/:FloID/trigger/:TriggerID/execute (handler triggerFlo).
//
// The header X-Flomation-Parent-Execution-ID is service-to-service
// only — it links a newly-triggered execution to its caller so the
// hierarchical executions list and breadcrumb view can stitch trees
// back together. Four branches need coverage:
//
//   1. External request + header → header ignored (no parent set).
//      External callers cannot forge parentage.
//   2. Internal request + valid parent → ParentLink populated with
//      the header value and "remote_trigger" relationship.
//   3. Internal request + unknown parent → silently dropped (so a
//      stale header from a since-deleted parent doesn't 4xx).
//   4. Internal request + no header → no parent (standard root
//      execution).
//
// The internal/external distinction is modelled by setting the
// "internal_mtls" gin context key in a middleware — that mirrors what
// the production internalEngine middleware in service.go does.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// recordingTriggerMock embeds the shared mockPersistence and adds a
// recording TriggerExecution implementation so each test can inspect
// the parent argument the handler chose to pass.
type recordingTriggerMock struct {
	*mockPersistence
	gotParent      *persistence.ParentLink
	gotCalls       int
	triggerErr     error
	returnedExecID string
}

func newRecordingTriggerMock() *recordingTriggerMock {
	return &recordingTriggerMock{
		mockPersistence: newMockPersistence(),
		returnedExecID:  "exec-new",
	}
}

func (m *recordingTriggerMock) TriggerExecution(_, _ string, _ interface{}, _ string, parent *persistence.ParentLink) (*string, error) {
	m.gotCalls++
	m.gotParent = parent
	if m.triggerErr != nil {
		return nil, m.triggerErr
	}
	id := m.returnedExecID
	return &id, nil
}

// setupTriggerFloRouter wires triggerFlo behind a middleware that can
// toggle the "internal_mtls" flag per-test. Mirrors how the production
// service registers the handler on both engines.
func setupTriggerFloRouter(svc *Service, markInternal bool) *gin.Engine {
	r := gin.New()
	r.POST("/api/v1/flo/:FloID/trigger/:TriggerID/execute",
		func(c *gin.Context) {
			if markInternal {
				c.Set("internal_mtls", true)
			}
			c.Next()
		},
		svc.triggerFlo,
	)
	return r
}

func makeTriggerRequest(t *testing.T, router *gin.Engine, parentHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/flo/flo-1/trigger/trigger-1/execute",
		bytes.NewReader([]byte(`{}`)),
	)
	req.Header.Set("Content-Type", "application/json")
	if parentHeader != "" {
		req.Header.Set(ParentExecutionHeader, parentHeader)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestTriggerFlo_ExternalRequest_IgnoresParentHeader(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newRecordingTriggerMock()
	// Even if the parent exists, an external request must not link.
	mock.executions["exec-parent"] = &api.Execution{ID: "exec-parent"}

	svc := setupTestService(mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()

	router := setupTriggerFloRouter(svc, false)
	rec := makeTriggerRequest(t, router, "exec-parent")

	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotCalls).To(Equal(1))
	Expect(mock.gotParent).To(BeNil(), "external requests must not propagate parent header")
}

func TestTriggerFlo_InternalRequest_ValidParent_PropagatesLink(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newRecordingTriggerMock()
	mock.executions["exec-parent"] = &api.Execution{ID: "exec-parent"}

	svc := setupTestService(mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()

	router := setupTriggerFloRouter(svc, true)
	rec := makeTriggerRequest(t, router, "exec-parent")

	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotCalls).To(Equal(1))
	Expect(mock.gotParent).NotTo(BeNil(), "internal request with a valid parent must propagate a ParentLink")
	Expect(mock.gotParent.ExecutionID).To(Equal("exec-parent"))
	Expect(mock.gotParent.Relationship).To(Equal("remote_trigger"))
}

func TestTriggerFlo_InternalRequest_UnknownParent_SilentlyDropped(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newRecordingTriggerMock()
	// No "exec-missing" entry — GetExecutionByID returns nil.

	svc := setupTestService(mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()

	router := setupTriggerFloRouter(svc, true)
	rec := makeTriggerRequest(t, router, "exec-missing")

	Expect(rec.Code).To(Equal(http.StatusCreated), "unknown parent must not fail the trigger")
	Expect(mock.gotCalls).To(Equal(1))
	Expect(mock.gotParent).To(BeNil(), "unknown parent IDs must be dropped, not threaded through")
}

func TestTriggerFlo_InternalRequest_NoHeader_NoParent(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newRecordingTriggerMock()

	svc := setupTestService(mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()

	router := setupTriggerFloRouter(svc, true)
	rec := makeTriggerRequest(t, router, "")

	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotCalls).To(Equal(1))
	Expect(mock.gotParent).To(BeNil())
}

// TestIsInternalRequest_DefaultsToFalse is a tiny guard on the helper —
// a request with no middleware tag must read as external. Without this
// invariant the gating becomes "everything is internal in dev mode",
// which would silently re-enable parent-header forging.
func TestIsInternalRequest_DefaultsToFalse(t *testing.T) {
	RegisterTestingT(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	Expect(isInternalRequest(c)).To(BeFalse())

	c.Set("internal_mtls", true)
	Expect(isInternalRequest(c)).To(BeTrue())
}
