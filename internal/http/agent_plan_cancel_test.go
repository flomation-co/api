package http

// HTTP-layer tests for the M3 cancel endpoints. Auth + cross-agent
// guard + outcome→status mapping verified deterministically via
// the mock persistence pattern; the transactional cancel behaviour
// is exercised end-to-end by the demo runbook against a real DB.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// cancelMock embeds the shared mockPersistence and overrides the
// methods cancel endpoints call. Captures inputs so tests can
// assert the right plan_id + reason landed in the persistence
// layer.
type cancelMock struct {
	*mockPersistence

	// Programmed returns.
	agent       *api.Agent
	plan        *api.Plan
	tasks       []*api.PlanTask
	cancelErr   error
	outcome     persistence.CancelOutcome
	getPlanErr  error
	getAgentErr error

	// Captured.
	gotPlanID    string
	gotReason    string
	cancelCalled bool
}

func newCancelMock() *cancelMock {
	return &cancelMock{
		mockPersistence: newMockPersistence(),
		agent: &api.Agent{
			ID:   "agent-1",
			Name: "Test Agent",
		},
		plan: &api.Plan{
			ID:      "plan-1",
			AgentID: "agent-1",
			Title:   "Test plan",
			Status:  "active",
		},
		outcome: persistence.CancelOutcomeCancelled,
	}
}

func (m *cancelMock) GetAgentByID(id string) (*api.Agent, error) {
	if m.getAgentErr != nil {
		return nil, m.getAgentErr
	}
	return m.agent, nil
}

func (m *cancelMock) GetPlanByID(id string) (*api.Plan, error) {
	if m.getPlanErr != nil {
		return nil, m.getPlanErr
	}
	return m.plan, nil
}

func (m *cancelMock) GetPlanTasksByPlanID(planID string) ([]*api.PlanTask, error) {
	return m.tasks, nil
}

func (m *cancelMock) CancelPlan(ctx context.Context, planID, reason string) (persistence.CancelOutcome, error) {
	m.cancelCalled = true
	m.gotPlanID = planID
	m.gotReason = reason
	if m.cancelErr != nil {
		return persistence.CancelOutcomeNotFound, m.cancelErr
	}
	return m.outcome, nil
}

func newCancelService(mock *cancelMock) *Service {
	// Seed user lookup so canAccessAgent passes the JWT path. The
	// internal endpoints don't need this but the JWT cancel does.
	mock.users = map[string]*api.User{
		"user-andy": {
			ID:            "user-andy",
			Organisations: []api.Organisation{{ID: "org-1"}},
		},
	}
	if mock.agent != nil {
		org := "org-1"
		mock.agent.OrganisationID = &org
	}
	return &Service{persistence: mock}
}

func setupCancelRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// JWT-emulating middleware
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Next()
	})
	r.POST("/api/v1/agent/:id/plan/:planID/cancel", svc.cancelAgentPlan)
	r.POST("/api/v1/internal/agent/:id/plan/:planID/cancel", svc.cancelAgentPlanInternal)
	r.GET("/api/v1/internal/agent/:id/plan/:planID", svc.getAgentPlanInternal)
	return r
}

// === JWT cancel endpoint ==================================

func TestCancelAgentPlan_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	body := `{"reason":"user changed their mind"}`
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/cancel",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.cancelCalled).To(BeTrue())
	Expect(mock.gotPlanID).To(Equal("plan-1"))
	Expect(mock.gotReason).To(Equal("user changed their mind"))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["plan_id"]).To(Equal("plan-1"))
	Expect(resp["outcome"]).To(Equal("cancelled"))
}

func TestCancelAgentPlan_NoBody_AllowedNoReason(t *testing.T) {
	// The editor's confirmation dialog allows users to skip the
	// reason. An empty body must still proceed (with reason="").
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/cancel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.cancelCalled).To(BeTrue())
	Expect(mock.gotReason).To(Equal(""))
}

func TestCancelAgentPlan_IdempotentOutcome_Returns200(t *testing.T) {
	// A user double-clicking cancel, or cancelling an already-
	// completed plan, returns 200/idempotent rather than an error.
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.outcome = persistence.CancelOutcomeIdempotent
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/cancel",
		bytes.NewReader([]byte(`{"reason":"again"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["outcome"]).To(Equal("idempotent"))
}

func TestCancelAgentPlan_CrossAgent_Returns404(t *testing.T) {
	// User has access to agent-1, but the plan_id they pass belongs
	// to a different agent. Surface 404 (not 403) to avoid leaking
	// the plan's existence.
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.plan.AgentID = "different-agent"
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/cancel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.cancelCalled).To(BeFalse()) // never reached persistence
}

func TestCancelAgentPlan_CrossOrgAgent_Returns403(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	svc := newCancelService(mock)
	other := "other-org"
	mock.agent.OrganisationID = &other
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/cancel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusForbidden))
	Expect(mock.cancelCalled).To(BeFalse())
}

func TestCancelAgentPlan_PlanNotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.getPlanErr = persistence.ErrPlanNotFound
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/missing/cancel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

// === Internal (mTLS) cancel endpoint ======================

func TestCancelAgentPlanInternal_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/agent-1/plan/plan-1/cancel",
		bytes.NewReader([]byte(`{"reason":"AI decided"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.cancelCalled).To(BeTrue())
	Expect(mock.gotReason).To(Equal("AI decided"))
}

func TestCancelAgentPlanInternal_CrossAgent_Returns404(t *testing.T) {
	// THIS IS THE LOAD-BEARING TEST. mTLS proves "an executor",
	// not "the right executor". A compromised agent must not be
	// able to cancel a different agent's plan by passing the
	// other agent's plan_id with its own agent_id in the URL —
	// the plan.agent_id == :id guard is the only defence.
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.plan.AgentID = "other-agent" // plan belongs to a different agent
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/agent-1/plan/plan-1/cancel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.cancelCalled).To(BeFalse()) // never reached persistence
}

// === Internal get-status endpoint =========================

func TestGetAgentPlanInternal_HappyPath_ReturnsBundle(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.tasks = []*api.PlanTask{
		{ID: "task-1", Name: "discover", Status: "completed"},
		{ID: "task-2", Name: "expand", Status: "in_progress"},
	}
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/internal/agent/agent-1/plan/plan-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var resp map[string]json.RawMessage
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp).To(HaveKey("plan"))
	Expect(resp).To(HaveKey("tasks"))

	var tasks []api.PlanTask
	Expect(json.Unmarshal(resp["tasks"], &tasks)).To(Succeed())
	Expect(tasks).To(HaveLen(2))
}

func TestGetAgentPlanInternal_CrossAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newCancelMock()
	mock.plan.AgentID = "other-agent"
	svc := newCancelService(mock)
	r := setupCancelRouter(svc)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/internal/agent/agent-1/plan/plan-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
}
