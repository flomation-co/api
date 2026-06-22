package http

// Lifecycle chain test for Agent Planning M1 + M1.5. Drives the
// four HTTP endpoints in sequence — create → tick → block →
// re-tick — using a single combined mock so each step's effect
// flows into the next. Proves the HTTP contracts compose: the
// shape one handler emits is the shape the next handler accepts.
//
// This is NOT a substitute for the real-DB demo runbook (which
// catches schema drift, the trigger-sync underscore-to-hyphen,
// runner plan_task_id forwarding, and the actual orchestrator
// dispatch). It IS the smallest assertion that the HTTP layer's
// orchestration logic and outcome mappings line up correctly.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// lifecycleMock unifies the create, tick, and block surfaces a
// single test scenario needs. Each method records what was passed
// and lets the test program subsequent return values to simulate
// the orchestrator's state-machine progression.
type lifecycleMock struct {
	*mockPersistence

	// Created plan (captured from CreatePlanWithTasks).
	gotPlan  *api.Plan
	gotTasks []*api.PlanTask

	// Programmed tick returns. The test sets these to simulate the
	// real persistence's progression through pending → in_progress →
	// completed/failed → blocked.
	tickResults []*persistence.TickPlanResult
	tickCalls   int

	// Block path.
	blockedTaskID string
	blockedReason string
	blockOutcome  persistence.BlockOutcome
}

func newLifecycleMock() *lifecycleMock {
	return &lifecycleMock{
		mockPersistence: newMockPersistence(),
		blockOutcome:    persistence.BlockOutcomeBlocked,
	}
}

func (m *lifecycleMock) VerifyFlowRevision(_, _ string) (bool, error) {
	return true, nil
}

func (m *lifecycleMock) CreatePlanWithTasks(plan *api.Plan, tasks []*api.PlanTask) error {
	plan.ID = "plan-1"
	m.gotPlan = plan
	m.gotTasks = tasks
	return nil
}

func (m *lifecycleMock) SetPlanNextCheck(_ string, _ time.Time) error { return nil }

func (m *lifecycleMock) CreatePlanEvent(_ *api.PlanEvent) error { return nil }

func (m *lifecycleMock) TickPlan(_ context.Context, planID string) (*persistence.TickPlanResult, error) {
	idx := m.tickCalls
	m.tickCalls++
	if idx < len(m.tickResults) {
		return m.tickResults[idx], nil
	}
	return &persistence.TickPlanResult{PlanID: planID, PlanStatus: "active"}, nil
}

func (m *lifecycleMock) BlockPlanTask(_ context.Context, planTaskID, reason string) (persistence.BlockOutcome, error) {
	m.blockedTaskID = planTaskID
	m.blockedReason = reason
	return m.blockOutcome, nil
}

// TestPlanLifecycle_CreateTickBlockReTick walks the full M1.5 happy-
// then-failing path through the HTTP layer. The test's value isn't
// that it catches a specific bug (the unit tests do that); it's
// that one developer reading this test gets the whole shape of the
// orchestration loop in 60 seconds.
func TestPlanLifecycle_CreateTickBlockReTick(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newLifecycleMock()
	svc := &Service{persistence: mock}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/agent/:id/plan", svc.createPlan)
	r.POST("/api/v1/internal/plan/:planID/tick", svc.tickPlan)
	r.POST("/api/v1/internal/plan_task/:planTaskID/block", svc.blockPlanTask)

	// === Step 1: create the plan ============================
	// Two orchestrator-kind tasks (no flow_id/flow_revision_id)
	// and one pinned-flow override. Confirms the M1.5 schema
	// changes are honoured by the wire shape — no flow ref means
	// orchestrator dispatch on the tick.
	createBody := `{
		"title":"Lifecycle test",
		"goal":"Walk every endpoint once",
		"tasks":[
			{"name":"discover","description":"Find three products."},
			{"name":"expand","description":"Pitch each one.","depends_on":["discover"]},
			{"name":"persist","flow_id":"f1","flow_revision_id":"r1","depends_on":["expand"]}
		]
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/agent/agent-1/plan",
		bytes.NewReader([]byte(createBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.gotPlan).NotTo(BeNil())
	Expect(mock.gotTasks).To(HaveLen(3))

	// Discriminator on the persisted shape — proof the validator
	// derived task_kind correctly for each task.
	Expect(mock.gotTasks[0].TaskKind).To(Equal(api.PlanTaskKindOrchestrator))
	Expect(mock.gotTasks[1].TaskKind).To(Equal(api.PlanTaskKindOrchestrator))
	Expect(mock.gotTasks[2].TaskKind).To(Equal(api.PlanTaskKindFlow))
	Expect(mock.gotTasks[0].FlowID).To(BeNil())
	Expect(mock.gotTasks[2].FlowID).NotTo(BeNil())

	// === Step 2: tick the plan (simulates the poller) =======
	// The mock returns a single Fired entry for the first task.
	// In real persistence this is the orchestrator-dispatch path
	// for an orchestrator-kind task — it creates a trigger_invocation
	// and an execution against the agent's orchestrator flow.
	mock.tickResults = []*persistence.TickPlanResult{
		{
			PlanID:     "plan-1",
			PlanStatus: "active",
			Fired: []persistence.FiredTask{
				{TaskID: "task-discover", TaskName: "discover", ExecutionID: "exec-1"},
			},
			Counts: map[string]int{"in_progress": 1, "pending": 2},
		},
		// Second tick (after block): plan transitions to blocked,
		// downstream tasks never dispatch.
		{
			PlanID:     "plan-1",
			PlanStatus: "blocked",
			Fired:      nil,
			Counts:     map[string]int{"failed": 1, "pending": 2},
		},
	}

	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/plan/plan-1/tick", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var firstTick persistence.TickPlanResult
	Expect(json.Unmarshal(rec.Body.Bytes(), &firstTick)).To(Succeed())
	Expect(firstTick.PlanStatus).To(Equal("active"))
	Expect(firstTick.Fired).To(HaveLen(1))
	Expect(firstTick.Fired[0].TaskName).To(Equal("discover"))

	// === Step 3: AI calls plan/block ========================
	// In the real loop this is the executor's plan/block action
	// posting from inside the dispatched orchestrator execution.
	// Here we hit the endpoint directly with the task ID the tick
	// just dispatched.
	blockBody := `{"reason":"missing the data source"}`
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/plan_task/task-discover/block",
		bytes.NewReader([]byte(blockBody)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.blockedTaskID).To(Equal("task-discover"))
	Expect(mock.blockedReason).To(Equal("missing the data source"))

	var blockResp map[string]interface{}
	Expect(json.Unmarshal(rec.Body.Bytes(), &blockResp)).To(Succeed())
	Expect(blockResp["outcome"]).To(Equal("blocked"))

	// === Step 4: re-tick — plan derives blocked =============
	// In real persistence, the BlockPlanTask call pokes
	// next_check_at = NOW so the next poller scan picks this plan
	// up immediately. derivePlanStatus then flips active → blocked
	// because there's a failed task with no remaining retries.
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/internal/plan/plan-1/tick", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	var secondTick persistence.TickPlanResult
	Expect(json.Unmarshal(rec.Body.Bytes(), &secondTick)).To(Succeed())
	Expect(secondTick.PlanStatus).To(Equal("blocked"))
	Expect(secondTick.Fired).To(BeEmpty()) // downstream tasks frozen
	Expect(secondTick.Counts["failed"]).To(Equal(1))
	Expect(secondTick.Counts["pending"]).To(Equal(2))

	// === Done ===============================================
	// Walked all four endpoints, confirmed each step's wire shape
	// fed cleanly into the next. The real-DB transactional
	// behaviour is validated by the demo runbook.
	Expect(mock.tickCalls).To(Equal(2))
}
