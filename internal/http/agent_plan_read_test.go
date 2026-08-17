package http

// HTTP-layer tests for the M2 plan-read endpoints. Mocks the
// persistence layer so the tests exercise validator + auth-scoping
// + response-shape behaviour deterministically. The transactional
// SQL behaviour is exercised end-to-end by the demo runbook.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// planReadMock records inputs to the list/detail/events methods and
// programs return values. Embeds the shared mockPersistence so it
// inherits every other method's no-op default.
type planReadMock struct {
	*mockPersistence

	// Programmed returns.
	agent          *api.Agent
	plan           *api.Plan
	tasks          []*api.PlanTask
	events         []*api.PlanEvent
	listPlans      []*api.Plan
	listPlansTotal int
	getAgentErr    error
	getPlanErr     error

	// Captured inputs.
	gotAgentID      string
	gotPlanID       string
	gotStatusFilter string
	gotLimit        int
	gotOffset       int
	gotBefore       *time.Time
}

func newPlanReadMock() *planReadMock {
	return &planReadMock{
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
		tasks: []*api.PlanTask{
			{ID: "task-1", PlanID: "plan-1", Name: "discover", Status: "completed"},
			{ID: "task-2", PlanID: "plan-1", Name: "expand", Status: "in_progress"},
		},
	}
}

func (m *planReadMock) GetAgentByID(id string) (*api.Agent, error) {
	m.gotAgentID = id
	if m.getAgentErr != nil {
		return nil, m.getAgentErr
	}
	return m.agent, nil
}

func (m *planReadMock) GetPlanByID(id string) (*api.Plan, error) {
	m.gotPlanID = id
	if m.getPlanErr != nil {
		return nil, m.getPlanErr
	}
	return m.plan, nil
}

func (m *planReadMock) GetPlanTasksByPlanID(planID string) ([]*api.PlanTask, error) {
	return m.tasks, nil
}

func (m *planReadMock) ListPlansByAgentID(agentID, statusFilter string, limit, offset int) ([]*api.Plan, int, error) {
	m.gotAgentID = agentID
	m.gotStatusFilter = statusFilter
	m.gotLimit = limit
	m.gotOffset = offset
	return m.listPlans, m.listPlansTotal, nil
}

func (m *planReadMock) ListPlanEventsByPlanID(planID string, limit int, before *time.Time) ([]*api.PlanEvent, error) {
	m.gotPlanID = planID
	m.gotLimit = limit
	m.gotBefore = before
	return m.events, nil
}

func setupPlanReadRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Mimic the production middleware that populates user context.
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Next()
	})
	r.GET("/api/v1/agent/:id/plan", svc.getAgentPlans)
	r.GET("/api/v1/agent/:id/plan/:planID", svc.getAgentPlan)
	r.GET("/api/v1/agent/:id/plan/:planID/event", svc.getAgentPlanEvents)
	return r
}

// planReadStubService wires the mock to a service + seeds the user
// table. canAccessAgent passes via the org-membership branch: the
// user is in org-1 and the agent declares org-1 as its owner.
// Tests that need a 404-on-missing-agent set mock.agent = nil and
// the stub leaves the (now nil) agent untouched.
func planReadStubService(mock *planReadMock) *Service {
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

// === list plans ===========================================

func TestGetAgentPlans_HappyPath_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	mock.listPlans = []*api.Plan{
		{ID: "p-1", AgentID: "agent-1", Title: "First"},
		{ID: "p-2", AgentID: "agent-1", Title: "Second"},
	}
	mock.listPlansTotal = 17
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan?limit=10&offset=20&status=active", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(rec.Header().Get("x-total-items")).To(Equal("17"))
	Expect(mock.gotAgentID).To(Equal("agent-1"))
	Expect(mock.gotStatusFilter).To(Equal("active"))
	Expect(mock.gotLimit).To(Equal(10))
	Expect(mock.gotOffset).To(Equal(20))

	var resp []api.Plan
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp).To(HaveLen(2))
	Expect(resp[0].Title).To(Equal("First"))
}

func TestGetAgentPlans_NoAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	mock.agent = nil // GetAgentByID returns nil
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/missing/plan", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

func TestGetAgentPlans_CrossOrgAgent_Returns403(t *testing.T) {
	// The agent belongs to a different org than the user — the
	// canAccessAgent check is the load-bearing guard that keeps
	// per-agent plan lists private across orgs.
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	svc := planReadStubService(mock)
	other := "other-org" // user has org-1 only
	mock.agent.OrganisationID = &other
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusForbidden))
}

// === plan detail ==========================================

func TestGetAgentPlan_HappyPath_ReturnsPlanAndTasks(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan/plan-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var resp map[string]json.RawMessage
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp).To(HaveKey("plan"))
	Expect(resp).To(HaveKey("tasks"))

	var plan api.Plan
	Expect(json.Unmarshal(resp["plan"], &plan)).To(Succeed())
	Expect(plan.ID).To(Equal("plan-1"))

	var tasks []api.PlanTask
	Expect(json.Unmarshal(resp["tasks"], &tasks)).To(Succeed())
	Expect(tasks).To(HaveLen(2))
	Expect(tasks[0].Name).To(Equal("discover"))
}

func TestGetAgentPlan_CrossAgentPlanID_Returns404(t *testing.T) {
	// Defence-in-depth: the user has access to agent-1, but the
	// plan_id they passed belongs to a different agent. We surface
	// 404 (not 403) so the response doesn't leak "this plan exists
	// elsewhere" — opaque from the caller's perspective.
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	mock.plan.AgentID = "different-agent"
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan/plan-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

func TestGetAgentPlan_PlanNotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	mock.getPlanErr = persistence.ErrPlanNotFound
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan/missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

// === events ===============================================

func TestGetAgentPlanEvents_HappyPath_ReturnsEvents(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	mock.events = []*api.PlanEvent{
		{ID: 1, PlanID: "plan-1", EventType: "plan_created"},
		{ID: 2, PlanID: "plan-1", EventType: "task_started"},
	}
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan/plan-1/event?limit=25", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.gotLimit).To(Equal(25))
	Expect(mock.gotBefore).To(BeNil())

	var resp []api.PlanEvent
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp).To(HaveLen(2))
}

func TestGetAgentPlanEvents_BeforeCursor_PassesParsedTime(t *testing.T) {
	// The ?before=<rfc3339> cursor lets the editor page backwards
	// through history. Confirm the handler parses it and threads
	// through to the persistence call.
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	url := "/api/v1/agent/agent-1/plan/plan-1/event?before=2026-06-22T14:00:00Z"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.gotBefore).NotTo(BeNil())
	Expect(mock.gotBefore.UTC().Hour()).To(Equal(14))
}

func TestGetAgentPlanEvents_MalformedBefore_Returns400(t *testing.T) {
	// Garbage in the ?before= parameter is the AI passing a bad
	// cursor or a developer typing the URL by hand. Clean 400 with
	// a detail field so the cause is obvious.
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanReadMock()
	svc := planReadStubService(mock)
	r := setupPlanReadRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/agent-1/plan/plan-1/event?before=not-a-time", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(strings.Contains(rec.Body.String(), "RFC3339")).To(BeTrue())
}
