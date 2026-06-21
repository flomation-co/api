package http

// Tests for the plan/create endpoint. The pure-function validator
// (validatePlanTasks) is exercised directly so each rule from the
// M1 plan doc lands as a named test. The HTTP wrapper is tested
// end-to-end against a recording mock so the wire shape, status
// codes, and persistence call ordering are pinned.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// === Pure validator tests ===

func TestValidatePlanTasks_DuplicateName(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: "f1", FlowRevisionID: "r1"},
		{Name: "a", FlowID: "f2", FlowRevisionID: "r2"},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("duplicate_task_name"))
	Expect(detail["task_name"]).To(Equal("a"))
}

func TestValidatePlanTasks_MissingName(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "", FlowID: "f1", FlowRevisionID: "r1"},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("missing_name"))
}

func TestValidatePlanTasks_SelfDependency(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "loop", FlowID: "f1", FlowRevisionID: "r1", DependsOn: []string{"loop"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("self_dependency"))
	Expect(detail["task_name"]).To(Equal("loop"))
}

func TestValidatePlanTasks_UnknownDependency(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "second", FlowID: "f1", FlowRevisionID: "r1", DependsOn: []string{"first"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("unknown_dependency"))
	Expect(detail["task_name"]).To(Equal("second"))
	Expect(detail["depends_on"]).To(Equal("first"))
}

func TestValidatePlanTasks_RefNotUpstream(t *testing.T) {
	// "later" references "sibling" in inputs but doesn't depend on it.
	// At tick time the ref wouldn't resolve because sibling hasn't run
	// yet — reject up-front so the agent can fix it.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "later", FlowID: "f1", FlowRevisionID: "r1",
			Inputs: json.RawMessage(`{"x":"${sibling.value}"}`)},
		{Name: "sibling", FlowID: "f2", FlowRevisionID: "r2"},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("ref_not_upstream"))
	Expect(detail["task_name"]).To(Equal("later"))
	Expect(detail["referenced"]).To(Equal("sibling"))
}

func TestValidatePlanTasks_RefToUpstreamIsAllowed(t *testing.T) {
	// Symmetric to the above: ref to a TRUE upstream task validates.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "upstream", FlowID: "f1", FlowRevisionID: "r1"},
		{Name: "consumer", FlowID: "f2", FlowRevisionID: "r2",
			DependsOn: []string{"upstream"},
			Inputs:    json.RawMessage(`{"x":"${upstream.value}"}`)},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

func TestValidatePlanTasks_TransitiveUpstreamIsAllowed(t *testing.T) {
	// A → B → C; C refs ${A.value}. A is transitively upstream of C.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "A", FlowID: "f", FlowRevisionID: "r"},
		{Name: "B", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"A"}},
		{Name: "C", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"B"},
			Inputs: json.RawMessage(`{"x":"${A.value}"}`)},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

func TestValidatePlanTasks_NonTaskNamespaceRefsPassValidation(t *testing.T) {
	// ${flow.X} / ${secrets.X} aren't task names; the validator must
	// leave them alone (executor handles them later). Without this,
	// validation would reject every plan that touches a runtime
	// namespace.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "send", FlowID: "f", FlowRevisionID: "r",
			Inputs: json.RawMessage(`{"channel":"${flow.channel_id}","key":"${secrets.api_key}"}`)},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

func TestValidatePlanTasks_Cycle(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"b"}},
		{Name: "b", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"a"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cycle"))
}

func TestValidatePlanTasks_TriangleCycle(t *testing.T) {
	// Larger cycle to make sure detectCycle handles >2 nodes.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"c"}},
		{Name: "b", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"a"}},
		{Name: "c", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"b"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cycle"))
}

func TestValidatePlanTasks_HappyPath(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "ingest", FlowID: "f", FlowRevisionID: "r"},
		{Name: "transform", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"ingest"},
			Inputs: json.RawMessage(`{"src":"${ingest.rows}"}`)},
		{Name: "send", FlowID: "f", FlowRevisionID: "r", DependsOn: []string{"transform"}},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

// === HTTP-level tests ===

// planRecordingMock embeds the shared mockPersistence and records the
// plan + tasks that landed in CreatePlanWithTasks, plus whether
// SetPlanNextCheck and CreatePlanEvent fired. The shared mock's
// no-op stubs are shadowed by these so the test can introspect.
type planRecordingMock struct {
	*mockPersistence
	verifyAlways    bool   // true = every VerifyFlowRevision returns true
	verifyMisses    string // when non-empty, the named revision id returns false
	gotPlan         *api.Plan
	gotTasks        []*api.PlanTask
	nextCheckCalled bool
	eventCalled     bool
	createErr       error
}

func newPlanRecordingMock() *planRecordingMock {
	return &planRecordingMock{
		mockPersistence: newMockPersistence(),
		verifyAlways:    true,
	}
}

func (m *planRecordingMock) VerifyFlowRevision(_, revisionID string) (bool, error) {
	if m.verifyMisses != "" && revisionID == m.verifyMisses {
		return false, nil
	}
	return m.verifyAlways, nil
}

func (m *planRecordingMock) CreatePlanWithTasks(plan *api.Plan, tasks []*api.PlanTask) error {
	if m.createErr != nil {
		return m.createErr
	}
	// Stamp a deterministic plan id so the test response can assert
	// against it (real persistence uses gen_random_uuid()).
	plan.ID = "plan-stamped-by-mock"
	m.gotPlan = plan
	m.gotTasks = tasks
	return nil
}

func (m *planRecordingMock) SetPlanNextCheck(planID string, _ time.Time) error {
	m.nextCheckCalled = true
	return nil
}

func (m *planRecordingMock) CreatePlanEvent(_ *api.PlanEvent) error {
	m.eventCalled = true
	return nil
}

func setupPlanRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/agent/:id/plan", svc.createPlan)
	return r
}

func postPlan(t *testing.T, router *gin.Engine, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/"+agentID+"/plan", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestCreatePlan_HappyPath_Returns201(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title": "Q3 Review",
		"goal":  "Pull metrics and summarise",
		"tasks": [
			{"name":"ingest","flow_id":"f1","flow_revision_id":"r1"},
			{"name":"summarise","flow_id":"f2","flow_revision_id":"r2",
			 "depends_on":["ingest"],
			 "inputs":{"rows":"${ingest.rows}"}}
		]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusCreated))

	var resp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp["plan_id"]).To(Equal("plan-stamped-by-mock"))
	Expect(resp["task_count"]).To(BeNumerically("==", 2))
	Expect(resp["status"]).To(Equal("active"))

	Expect(mock.gotPlan).NotTo(BeNil())
	Expect(mock.gotPlan.AgentID).To(Equal("agent-1"))
	Expect(mock.gotPlan.Status).To(Equal("active"))
	Expect(mock.gotTasks).To(HaveLen(2))

	// The depends_on field on the persisted second task should carry
	// the first task's UUID (handler-assigned, then DB-bound). The
	// mock can't assert the exact UUID but it can confirm the array
	// has one entry pointing somewhere.
	Expect(mock.gotTasks[1].DependsOn).To(HaveLen(1))
	Expect(mock.gotTasks[1].DependsOn[0]).To(Equal(mock.gotTasks[0].ID))

	Expect(mock.nextCheckCalled).To(BeTrue())
	Expect(mock.eventCalled).To(BeTrue())
}

func TestCreatePlan_DuplicateTaskName_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title": "x", "goal": "y",
		"tasks": [
			{"name":"a","flow_id":"f","flow_revision_id":"r"},
			{"name":"a","flow_id":"f","flow_revision_id":"r"}
		]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.gotPlan).To(BeNil())
}

func TestCreatePlan_UnknownFlowRevision_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	mock.verifyMisses = "r-missing"
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title": "x", "goal": "y",
		"tasks": [{"name":"a","flow_id":"f","flow_revision_id":"r-missing"}]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("unknown_flow_revision"))
	Expect(rec.Body.String()).To(ContainSubstring("r-missing"))
	Expect(mock.gotPlan).To(BeNil())
}

func TestCreatePlan_MalformedJSON_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	rec := postPlan(t, r, "agent-1", `{"title": invalid`)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("schema"))
}

func TestCreatePlan_MissingTitle_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{"goal":"y","tasks":[{"name":"a","flow_id":"f","flow_revision_id":"r"}]}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestCreatePlan_EmptyTasksArray_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{"title":"x","goal":"y","tasks":[]}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestCreatePlan_Cycle_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title":"x","goal":"y",
		"tasks":[
			{"name":"a","flow_id":"f","flow_revision_id":"r","depends_on":["b"]},
			{"name":"b","flow_id":"f","flow_revision_id":"r","depends_on":["a"]}
		]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("cycle"))
}
