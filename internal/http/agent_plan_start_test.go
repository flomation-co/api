package http

// HTTP-layer tests for the M4 start endpoints. Mirrors the M3
// cancel tests: mock persistence so the test exercises the
// validator + auth scoping + outcome-to-status mapping
// deterministically. Real transactional behaviour is covered by
// the demo runbook.

import (
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

type startMock struct {
	*mockPersistence

	agent       *api.Agent
	plan        *api.Plan
	startErr    error
	outcome     persistence.StartOutcome
	getPlanErr  error
	getAgentErr error

	gotPlanID   string
	startCalled bool
}

func newStartMock() *startMock {
	return &startMock{
		mockPersistence: newMockPersistence(),
		agent: &api.Agent{
			ID: "agent-1", Name: "Test Agent",
		},
		plan: &api.Plan{
			ID: "plan-1", AgentID: "agent-1", Title: "Test", Status: "draft",
		},
		outcome: persistence.StartOutcomeStarted,
	}
}

func (m *startMock) GetAgentByID(id string) (*api.Agent, error) {
	if m.getAgentErr != nil {
		return nil, m.getAgentErr
	}
	return m.agent, nil
}

func (m *startMock) GetPlanByID(id string) (*api.Plan, error) {
	if m.getPlanErr != nil {
		return nil, m.getPlanErr
	}
	return m.plan, nil
}

func (m *startMock) StartPlan(_ context.Context, planID string) (persistence.StartOutcome, error) {
	m.startCalled = true
	m.gotPlanID = planID
	if m.startErr != nil {
		return persistence.StartOutcomeNotFound, m.startErr
	}
	return m.outcome, nil
}

func newStartService(mock *startMock) *Service {
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

func setupStartRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Next()
	})
	r.POST("/api/v1/agent/:id/plan/:planID/start", svc.startAgentPlan)
	r.POST("/api/v1/internal/agent/:id/plan/:planID/start", svc.startAgentPlanInternal)
	return r
}

// === JWT start endpoint ==================================

func TestStartAgentPlan_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.startCalled).To(BeTrue())
	Expect(mock.gotPlanID).To(Equal("plan-1"))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["plan_id"]).To(Equal("plan-1"))
	Expect(resp["outcome"]).To(Equal("started"))
}

func TestStartAgentPlan_IdempotentOutcome_Returns200(t *testing.T) {
	// Starting an already-active plan returns 200/idempotent.
	// The editor's Start button could be clicked twice in rapid
	// succession — the second call must not error.
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	mock.outcome = persistence.StartOutcomeIdempotent
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["outcome"]).To(Equal("idempotent"))
}

func TestStartAgentPlan_AlreadyTerminal_Returns409(t *testing.T) {
	// Trying to start a completed or cancelled plan is a hard
	// error — the plan can never reach active again.
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	mock.outcome = persistence.StartOutcomeAlreadyTerminal
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusConflict))
	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["error"]).To(Equal("plan_already_terminal"))
}

func TestStartAgentPlan_CrossAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	mock.plan.AgentID = "different-agent"
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.startCalled).To(BeFalse())
}

func TestStartAgentPlan_PlanNotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	mock.getPlanErr = persistence.ErrPlanNotFound
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agent/agent-1/plan/missing/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

// === Internal (mTLS) start endpoint ======================

func TestStartAgentPlanInternal_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.startCalled).To(BeTrue())
}

func TestStartAgentPlanInternal_CrossAgent_Returns404(t *testing.T) {
	// Mirror of the cancel cross-agent guard: mTLS proves "an
	// executor", not "the right executor". A compromised agent
	// must not be able to start another agent's draft plan.
	t.Parallel()
	RegisterTestingT(t)
	mock := newStartMock()
	mock.plan.AgentID = "other-agent"
	svc := newStartService(mock)
	r := setupStartRouter(svc)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/agent-1/plan/plan-1/start", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.startCalled).To(BeFalse())
}
