package http

// HTTP-level tests for the plan/block endpoint. We mock the
// persistence layer so the test exercises validator + outcome-mapping
// behaviour deterministically (the transactional behaviour itself is
// covered by the persistence package's writeback test suite and by
// end-to-end manual validation per the M1 demo runbook).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// blockRecordingMock embeds the shared mockPersistence and captures
// the (planTaskID, reason) the handler forwarded so the test can
// assert verbatim. The outcome the mock returns is programmable so
// each test exercises a specific outcome-to-status mapping.
type blockRecordingMock struct {
	*mockPersistence

	gotPlanTaskID string
	gotReason     string
	outcome       persistence.BlockOutcome
	err           error
}

func newBlockRecordingMock() *blockRecordingMock {
	return &blockRecordingMock{
		mockPersistence: newMockPersistence(),
		outcome:         persistence.BlockOutcomeBlocked,
	}
}

func (m *blockRecordingMock) BlockPlanTask(_ context.Context, planTaskID, reason string) (persistence.BlockOutcome, error) {
	m.gotPlanTaskID = planTaskID
	m.gotReason = reason
	if m.err != nil {
		return persistence.BlockOutcomeNotFound, m.err
	}
	return m.outcome, nil
}

func setupBlockRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/plan_task/:planTaskID/block", svc.blockPlanTask)
	return r
}

func postBlock(t *testing.T, router *gin.Engine, planTaskID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/plan_task/"+planTaskID+"/block",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestBlockPlanTask_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc",
		`{"reason":"missing BigQuery credentials"}`)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.gotPlanTaskID).To(Equal("task-abc"))
	Expect(mock.gotReason).To(Equal("missing BigQuery credentials"))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["plan_task_id"]).To(Equal("task-abc"))
	Expect(resp["outcome"]).To(Equal("blocked"))
}

func TestBlockPlanTask_IdempotentOutcome_Returns200(t *testing.T) {
	// Calling plan/block on an already-terminal task is a no-op the
	// AI must be allowed to make freely — repeats across runner retries
	// shouldn't surface as errors the model has to reason about.
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	mock.outcome = persistence.BlockOutcomeIdempotent
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc", `{"reason":"giving up"}`)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["outcome"]).To(Equal("idempotent"))
}

func TestBlockPlanTask_NotFoundOutcome_Returns404(t *testing.T) {
	// A junk plan_task_id is a clean 404 — the executor action
	// surfaces this as a tool_result the AI can self-correct from.
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	mock.outcome = persistence.BlockOutcomeNotFound
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-missing", `{"reason":"x"}`)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["error"]).To(Equal("plan_task_not_found"))
	Expect(resp["plan_task_id"]).To(Equal("task-missing"))
}

func TestBlockPlanTask_MissingReason_Returns400(t *testing.T) {
	// reason is mandatory — calling plan/block without a reason means
	// the AI hasn't articulated why it's stuck, which is the whole
	// signal a human-facing "blocked" state needs to carry.
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc", `{"reason":""}`)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.gotPlanTaskID).To(Equal("")) // persistence not called
}

func TestBlockPlanTask_WhitespaceReason_Returns400(t *testing.T) {
	// "   " is treated the same as empty — TrimSpace guards against
	// a model that pads its tool input.
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc", `{"reason":"   \t  "}`)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.gotPlanTaskID).To(Equal(""))
}

func TestBlockPlanTask_MalformedJSON_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc", `not-json`)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestBlockPlanTask_PersistenceError_Returns500(t *testing.T) {
	// Unexpected persistence errors must NOT leak detail to the
	// caller. The action will retry, and a 500 surfaces as a generic
	// retryable error to the AI's tool result.
	t.Parallel()
	RegisterTestingT(t)
	mock := newBlockRecordingMock()
	mock.err = context.DeadlineExceeded
	svc := &Service{persistence: mock}
	r := setupBlockRouter(svc)

	rec := postBlock(t, r, "task-abc", `{"reason":"x"}`)
	Expect(rec.Code).To(Equal(http.StatusInternalServerError))
}
