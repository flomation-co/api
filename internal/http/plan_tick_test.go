package http

// HTTP-layer tests for the tick endpoint. The persistence
// transactional logic is exercised end-to-end at M1 commit 9; here
// we pin the status-code mapping that the Launch poller depends on.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// tickRecordingMock embeds the shared mockPersistence and lets each
// test program a specific TickPlan return.
type tickRecordingMock struct {
	*mockPersistence
	result *persistence.TickPlanResult
	err    error
	calls  int
}

func newTickMock() *tickRecordingMock {
	return &tickRecordingMock{mockPersistence: newMockPersistence()}
}

func (m *tickRecordingMock) TickPlan(_ context.Context, planID string) (*persistence.TickPlanResult, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &persistence.TickPlanResult{PlanID: planID, PlanStatus: "active"}, nil
}

func setupTickRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/plan/:planID/tick", svc.tickPlan)
	return r
}

func postTick(t *testing.T, r *gin.Engine, planID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/plan/"+planID+"/tick", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTickPlan_HappyPath_Returns200WithResult(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	mock.result = &persistence.TickPlanResult{
		PlanID:     "plan-1",
		PlanStatus: "active",
		Fired: []persistence.FiredTask{
			{TaskID: "t1", TaskName: "ingest", ExecutionID: "exec-1"},
		},
		Counts: map[string]int{"pending": 1, "in_progress": 1},
	}
	svc := &Service{persistence: mock}
	r := setupTickRouter(svc)

	rec := postTick(t, r, "plan-1")
	Expect(rec.Code).To(Equal(http.StatusOK))

	var resp persistence.TickPlanResult
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp.PlanID).To(Equal("plan-1"))
	Expect(resp.PlanStatus).To(Equal("active"))
	Expect(resp.Fired).To(HaveLen(1))
	Expect(resp.Fired[0].TaskName).To(Equal("ingest"))
	Expect(mock.calls).To(Equal(1))
}

func TestTickPlan_PlanNotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	mock.err = persistence.ErrPlanNotFound
	svc := &Service{persistence: mock}
	r := setupTickRouter(svc)
	rec := postTick(t, r, "ghost")
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

func TestTickPlan_TerminalPlan_Returns204(t *testing.T) {
	// A completed or cancelled plan shouldn't be tickable; the 204
	// tells the poller to stop bothering this plan.
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	mock.err = persistence.ErrPlanTerminal
	svc := &Service{persistence: mock}
	r := setupTickRouter(svc)
	rec := postTick(t, r, "plan-done")
	Expect(rec.Code).To(Equal(http.StatusNoContent))
}

func TestTickPlan_Locked_Returns409(t *testing.T) {
	// Two pollers racing on the same plan — the loser gets 409 so it
	// knows to back off instead of retrying immediately.
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	mock.err = persistence.ErrPlanLocked
	svc := &Service{persistence: mock}
	r := setupTickRouter(svc)
	rec := postTick(t, r, "plan-busy")
	Expect(rec.Code).To(Equal(http.StatusConflict))
}

func TestTickPlan_UnexpectedError_Returns500(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	mock.err = errors.New("database is on fire")
	svc := &Service{persistence: mock}
	r := setupTickRouter(svc)
	rec := postTick(t, r, "plan-1")
	Expect(rec.Code).To(Equal(http.StatusInternalServerError))
}

func TestTickPlan_MissingPlanID_Returns400(t *testing.T) {
	// Gin's :planID route param means an empty path segment routes
	// elsewhere — what we're actually pinning here is the handler's
	// defensive guard for when a caller manages to invoke it
	// directly with a blank param.
	t.Parallel()
	RegisterTestingT(t)
	mock := newTickMock()
	svc := &Service{persistence: mock}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Register a route that captures an empty :planID by forwarding
	// to the handler with an empty param value. This isn't reachable
	// in production routing, but the handler's guard exists for
	// defence-in-depth.
	r.POST("/manual-tick", svc.tickPlan)
	req := httptest.NewRequest(http.MethodPost, "/manual-tick", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}
