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

// ptr lifts a string literal to a *string for createPlanTask
// field initialisation. M1.5 made FlowID and FlowRevisionID nullable
// (orchestrator-kind tasks leave them nil); tests using the flow
// override path need this little helper.
func ptr(s string) *string { return &s }

// === Pure validator tests ===

func TestValidatePlanTasks_DuplicateName(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: ptr("f1"), FlowRevisionID: ptr("r1")},
		{Name: "a", FlowID: ptr("f2"), FlowRevisionID: ptr("r2")},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("duplicate_task_name"))
	Expect(detail["task_name"]).To(Equal("a"))
}

func TestValidatePlanTasks_MissingName(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "", FlowID: ptr("f1"), FlowRevisionID: ptr("r1")},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("missing_name"))
}

func TestValidatePlanTasks_SelfDependency(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "loop", FlowID: ptr("f1"), FlowRevisionID: ptr("r1"), DependsOn: []string{"loop"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("self_dependency"))
	Expect(detail["task_name"]).To(Equal("loop"))
}

func TestValidatePlanTasks_UnknownDependency(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "second", FlowID: ptr("f1"), FlowRevisionID: ptr("r1"), DependsOn: []string{"first"}},
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
		{Name: "later", FlowID: ptr("f1"), FlowRevisionID: ptr("r1"),
			Inputs: json.RawMessage(`{"x":"${sibling.value}"}`)},
		{Name: "sibling", FlowID: ptr("f2"), FlowRevisionID: ptr("r2")},
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
		{Name: "upstream", FlowID: ptr("f1"), FlowRevisionID: ptr("r1")},
		{Name: "consumer", FlowID: ptr("f2"), FlowRevisionID: ptr("r2"),
			DependsOn: []string{"upstream"},
			Inputs:    json.RawMessage(`{"x":"${upstream.value}"}`)},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

func TestValidatePlanTasks_TransitiveUpstreamIsAllowed(t *testing.T) {
	// A → B → C; C refs ${A.value}. A is transitively upstream of C.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "A", FlowID: ptr("f"), FlowRevisionID: ptr("r")},
		{Name: "B", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"A"}},
		{Name: "C", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"B"},
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
		{Name: "send", FlowID: ptr("f"), FlowRevisionID: ptr("r"),
			Inputs: json.RawMessage(`{"channel":"${flow.channel_id}","key":"${secrets.api_key}"}`)},
	}
	Expect(validatePlanTasks(tasks)).To(BeNil())
}

func TestValidatePlanTasks_Cycle(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"b"}},
		{Name: "b", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"a"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cycle"))
}

func TestValidatePlanTasks_TriangleCycle(t *testing.T) {
	// Larger cycle to make sure detectCycle handles >2 nodes.
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "a", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"c"}},
		{Name: "b", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"a"}},
		{Name: "c", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"b"}},
	}
	detail := validatePlanTasks(tasks)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cycle"))
}

func TestValidatePlanTasks_HappyPath(t *testing.T) {
	RegisterTestingT(t)
	tasks := []createPlanTask{
		{Name: "ingest", FlowID: ptr("f"), FlowRevisionID: ptr("r")},
		{Name: "transform", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"ingest"},
			Inputs: json.RawMessage(`{"src":"${ingest.rows}"}`)},
		{Name: "send", FlowID: ptr("f"), FlowRevisionID: ptr("r"), DependsOn: []string{"transform"}},
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

// === M1.5 commit 5: orchestrator-default validator tests ===

func TestDeriveTaskKind_NoFlowID_DefaultsToOrchestrator(t *testing.T) {
	RegisterTestingT(t)
	kind, errDetail := deriveTaskKind(createPlanTask{Name: "x"})
	Expect(errDetail).To(BeNil())
	Expect(kind).To(Equal("orchestrator"))
}

func TestDeriveTaskKind_BothFlowFieldsSet_IsFlowKind(t *testing.T) {
	RegisterTestingT(t)
	kind, errDetail := deriveTaskKind(createPlanTask{
		Name:           "x",
		FlowID:         ptr("f"),
		FlowRevisionID: ptr("r"),
	})
	Expect(errDetail).To(BeNil())
	Expect(kind).To(Equal("flow"))
}

func TestDeriveTaskKind_OnlyFlowID_RejectedAsPartial(t *testing.T) {
	// Asymmetric: flow_id without flow_revision_id is meaningless —
	// the agent is half-way through pinning a flow and the validator
	// surfaces the partial state so they can complete it.
	RegisterTestingT(t)
	kind, errDetail := deriveTaskKind(createPlanTask{
		Name:   "x",
		FlowID: ptr("f"),
	})
	Expect(kind).To(Equal(""))
	Expect(errDetail).NotTo(BeNil())
	Expect(errDetail["reason"]).To(Equal("partial_flow_ref"))
	Expect(errDetail["task_name"]).To(Equal("x"))
}

func TestDeriveTaskKind_OnlyFlowRevisionID_RejectedAsPartial(t *testing.T) {
	RegisterTestingT(t)
	_, errDetail := deriveTaskKind(createPlanTask{
		Name:           "x",
		FlowRevisionID: ptr("r"),
	})
	Expect(errDetail).NotTo(BeNil())
	Expect(errDetail["reason"]).To(Equal("partial_flow_ref"))
}

func TestDeriveTaskKind_EmptyStringPointersAreOrchestrator(t *testing.T) {
	// Edge case — a wire client that JSON-marshals with empty strings
	// instead of omitting the field. We treat empty as absent so the
	// orchestrator default fires; the alternative would be a
	// confusing 400 over a benign serialisation choice.
	RegisterTestingT(t)
	empty := ""
	kind, errDetail := deriveTaskKind(createPlanTask{
		Name:           "x",
		FlowID:         &empty,
		FlowRevisionID: &empty,
	})
	Expect(errDetail).To(BeNil())
	Expect(kind).To(Equal("orchestrator"))
}

// HTTP-layer integration: orchestrator-kind tasks ship without
// flow_id / flow_revision_id and don't trigger VerifyFlowRevision.
func TestCreatePlan_OrchestratorKindTasksAccepted(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title": "Demo",
		"goal":  "Run two orchestrator-kind tasks",
		"tasks": [
			{"name":"a","description":"first step"},
			{"name":"b","description":"second step","depends_on":["a"]}
		]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotPlan).NotTo(BeNil())
	Expect(mock.gotTasks).To(HaveLen(2))
	Expect(mock.gotTasks[0].TaskKind).To(Equal("orchestrator"))
	Expect(mock.gotTasks[0].FlowID).To(BeNil())
	Expect(mock.gotTasks[0].FlowRevisionID).To(BeNil())
	Expect(mock.gotTasks[1].TaskKind).To(Equal("orchestrator"))
}

func TestCreatePlan_MixedKinds_BothShapesAccepted(t *testing.T) {
	// A plan with one orchestrator-kind task and one flow-kind task
	// — the escape-hatch case the M1.5 design exists to support.
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	body := `{
		"title": "Mixed",
		"goal":  "AI does the analysis, curated flow does the delivery",
		"tasks": [
			{"name":"analyse","description":"summarise the data"},
			{"name":"deliver","flow_id":"flow-1","flow_revision_id":"rev-1","depends_on":["analyse"]}
		]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotTasks).To(HaveLen(2))
	Expect(mock.gotTasks[0].TaskKind).To(Equal("orchestrator"))
	Expect(mock.gotTasks[0].FlowID).To(BeNil())
	Expect(mock.gotTasks[1].TaskKind).To(Equal("flow"))
	Expect(*mock.gotTasks[1].FlowID).To(Equal("flow-1"))
	Expect(*mock.gotTasks[1].FlowRevisionID).To(Equal("rev-1"))
}

func TestCreatePlan_PartialFlowRef_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	mock := newPlanRecordingMock()
	svc := &Service{persistence: mock}
	r := setupPlanRouter(svc)

	// flow_id present, flow_revision_id missing → partial_flow_ref.
	body := `{
		"title": "x", "goal": "y",
		"tasks": [{"name":"a","flow_id":"f"}]
	}`
	rec := postPlan(t, r, "agent-1", body)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("partial_flow_ref"))
	Expect(mock.gotPlan).To(BeNil())
}
