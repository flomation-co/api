package http

// HTTP-layer tests for the M5 revise endpoints. Pinned outcome →
// status mapping, cross-agent guard, empty-revision rejection.

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

type reviseMock struct {
	*mockPersistence

	agent      *api.Agent
	plan       *api.Plan
	result     persistence.RevisionResult
	reviseErr  error
	getPlanErr error

	gotPlanID   string
	gotOps      persistence.RevisionOps
	called      bool
}

func newReviseMock() *reviseMock {
	return &reviseMock{
		mockPersistence: newMockPersistence(),
		agent: &api.Agent{ID: "agent-1", Name: "Test Agent"},
		plan: &api.Plan{
			ID: "plan-1", AgentID: "agent-1", Title: "Test", Status: "draft",
		},
		result: persistence.RevisionResult{
			Outcome:   persistence.RevisionOutcomeRevised,
			NewStatus: "draft",
		},
	}
}

func (m *reviseMock) GetAgentByID(id string) (*api.Agent, error) {
	return m.agent, nil
}

func (m *reviseMock) GetPlanByID(id string) (*api.Plan, error) {
	if m.getPlanErr != nil {
		return nil, m.getPlanErr
	}
	return m.plan, nil
}

func (m *reviseMock) RevisePlan(_ context.Context, planID string, ops persistence.RevisionOps) (persistence.RevisionResult, error) {
	m.called = true
	m.gotPlanID = planID
	m.gotOps = ops
	if m.reviseErr != nil {
		return persistence.RevisionResult{Outcome: persistence.RevisionOutcomeNotFound}, m.reviseErr
	}
	return m.result, nil
}

func newReviseService(mock *reviseMock) *Service {
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

func setupReviseRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Next()
	})
	r.POST("/api/v1/agent/:id/plan/:planID/revise", svc.reviseAgentPlan)
	r.POST("/api/v1/internal/agent/:id/plan/:planID/revise", svc.reviseAgentPlanInternal)
	return r
}

func postRevise(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// === JWT revise ==========================================

func TestReviseAgentPlan_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.result = persistence.RevisionResult{
		Outcome:   persistence.RevisionOutcomeRevised,
		NewStatus: "draft",
		AddedIDs:  []string{"new-task-id"},
	}
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"new_task","description":"do the thing"}]}`
	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/plan-1/revise", body)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.called).To(BeTrue())
	Expect(mock.gotPlanID).To(Equal("plan-1"))
	Expect(mock.gotOps.AddTasks).To(HaveLen(1))
	Expect(mock.gotOps.AddTasks[0].Name).To(Equal("new_task"))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["plan_id"]).To(Equal("plan-1"))
	Expect(resp["outcome"]).To(Equal("revised"))
	Expect(resp["new_status"]).To(Equal("draft"))
}

func TestReviseAgentPlan_EmptyOps_Returns400(t *testing.T) {
	// A revise request with nothing to do is a caller error — they
	// likely forgot to populate the body. Return 400 rather than
	// silently succeed.
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/plan-1/revise", `{}`)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.called).To(BeFalse())

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["error"]).To(Equal("empty_revision"))
}

func TestReviseAgentPlan_TerminalPlan_Returns409(t *testing.T) {
	// Completed / cancelled plans can't be modified. Surface as
	// 409 so callers know the plan reached a final state.
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.result = persistence.RevisionResult{
		Outcome:   persistence.RevisionOutcomeTerminal,
		NewStatus: "cancelled",
	}
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"remove_tasks":["x"]}`
	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/plan-1/revise", body)
	Expect(rec.Code).To(Equal(http.StatusConflict))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["error"]).To(Equal("plan_terminal"))
}

func TestReviseAgentPlan_InvalidValidation_Returns400WithDetail(t *testing.T) {
	// The persistence-layer validator returns structured detail
	// (cycle / unknown_dependency / partial_flow_ref / etc). The
	// HTTP layer must surface it verbatim so the AI / editor can
	// route to the offending task.
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.result = persistence.RevisionResult{
		Outcome: persistence.RevisionOutcomeInvalid,
		ErrorDetail: map[string]interface{}{
			"reason":    "cycle",
			"task_name": "a",
		},
	}
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"a","depends_on":["a"]}]}`
	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/plan-1/revise", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["error"]).To(Equal("validation"))
	detail := resp["detail"].(map[string]interface{})
	Expect(detail["reason"]).To(Equal("cycle"))
	Expect(detail["task_name"]).To(Equal("a"))
}

func TestReviseAgentPlan_CrossAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.plan.AgentID = "different-agent"
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"x"}]}`
	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/plan-1/revise", body)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.called).To(BeFalse())
}

func TestReviseAgentPlan_PlanNotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.getPlanErr = persistence.ErrPlanNotFound
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"x"}]}`
	rec := postRevise(t, r, "/api/v1/agent/agent-1/plan/missing/revise", body)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

// === Internal (mTLS) revise =============================

func TestReviseAgentPlanInternal_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.result = persistence.RevisionResult{
		Outcome:   persistence.RevisionOutcomeRevised,
		NewStatus: "active",
	}
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"new"}]}`
	rec := postRevise(t, r, "/api/v1/internal/agent/agent-1/plan/plan-1/revise", body)
	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.called).To(BeTrue())
}

func TestReviseAgentPlanInternal_CrossAgent_Returns404(t *testing.T) {
	// mTLS proves "an executor", not "the right executor". The
	// cross-agent guard is the only thing stopping a compromised
	// agent from rewriting another agent's plan graph.
	t.Parallel()
	RegisterTestingT(t)
	mock := newReviseMock()
	mock.plan.AgentID = "other-agent"
	svc := newReviseService(mock)
	r := setupReviseRouter(svc)

	body := `{"add_tasks":[{"name":"new"}]}`
	rec := postRevise(t, r, "/api/v1/internal/agent/agent-1/plan/plan-1/revise", body)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
	Expect(mock.called).To(BeFalse())
}
