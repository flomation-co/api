package persistence

// Pure-function tests for plan_revise.go. The transactional SQL
// behaviour is covered end-to-end by the M5 demo runbook against a
// real DB. What we pin here are the in-memory projection +
// validation helpers, since their correctness is what protects the
// graph integrity invariants.

import (
	"encoding/json"
	"testing"

	"github.com/lib/pq"
	. "github.com/onsi/gomega"

	api "flomation.app/automate/api"
)

func TestRevisionOutcomeConstants_StayStable(t *testing.T) {
	// Wire-format pin: outcome strings appear in HTTP responses and
	// AI tool_results. Renaming any of these is a wire-break.
	RegisterTestingT(t)
	Expect(string(RevisionOutcomeRevised)).To(Equal("revised"))
	Expect(string(RevisionOutcomeNotFound)).To(Equal("not_found"))
	Expect(string(RevisionOutcomeTerminal)).To(Equal("terminal"))
	Expect(string(RevisionOutcomeInvalid)).To(Equal("invalid"))
}

func TestUpdateTouchesNonDescriptionFields(t *testing.T) {
	// Description-only updates are allowed on non-pending tasks.
	// Anything else requires pending. This helper is the gatekeeper.
	RegisterTestingT(t)

	desc := "new description"
	descOnly := RevisionUpdate{Name: "x", Description: &desc}
	Expect(updateTouchesNonDescriptionFields(descOnly)).To(BeFalse())

	flowID := "flow-1"
	withFlow := RevisionUpdate{Name: "x", FlowID: &flowID}
	Expect(updateTouchesNonDescriptionFields(withFlow)).To(BeTrue())

	deps := []string{"dep1"}
	withDeps := RevisionUpdate{Name: "x", DependsOn: &deps}
	Expect(updateTouchesNonDescriptionFields(withDeps)).To(BeTrue())

	emptyDeps := []string{}
	withClearedDeps := RevisionUpdate{Name: "x", DependsOn: &emptyDeps}
	Expect(updateTouchesNonDescriptionFields(withClearedDeps)).To(BeTrue(),
		"clearing depends_on must count as a non-description change")

	withInputs := RevisionUpdate{Name: "x", Inputs: json.RawMessage(`{"a":1}`)}
	Expect(updateTouchesNonDescriptionFields(withInputs)).To(BeTrue())

	max := 3
	withMaxAttempts := RevisionUpdate{Name: "x", MaxAttempts: &max}
	Expect(updateTouchesNonDescriptionFields(withMaxAttempts)).To(BeTrue())
}

// === Pre-projection per-op rules ===

func TestPreProjectionChecks_RemoveInProgressRejected(t *testing.T) {
	// Removing an in_progress task is not allowed — its execution
	// is live; deleting the row would orphan the runner.
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "running", Status: "in_progress"},
	}
	byName := map[string]*api.PlanTask{"running": existing[0]}
	ops := RevisionOps{RemoveTasks: []string{"running"}}

	detail := preProjectionChecks(ops, existing, byName)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cannot_remove_task_in_status"))
	Expect(detail["task_status"]).To(Equal("in_progress"))
}

func TestPreProjectionChecks_RemoveCompletedRejected(t *testing.T) {
	// Completed tasks may have outputs other tasks depend on; their
	// removal would silently break downstream substitutions. Reject.
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "done", Status: "completed"},
	}
	byName := map[string]*api.PlanTask{"done": existing[0]}
	ops := RevisionOps{RemoveTasks: []string{"done"}}
	detail := preProjectionChecks(ops, existing, byName)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cannot_remove_task_in_status"))
}

func TestPreProjectionChecks_RemoveFailedAllowed(t *testing.T) {
	// Failed tasks ARE removable — this is the "fix a blocked plan"
	// path. Drop the failed task, optionally add a replacement.
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "failed_task", Status: "failed"},
	}
	byName := map[string]*api.PlanTask{"failed_task": existing[0]}
	ops := RevisionOps{RemoveTasks: []string{"failed_task"}}
	Expect(preProjectionChecks(ops, existing, byName)).To(BeNil())
}

func TestPreProjectionChecks_RemoveUnknownRejected(t *testing.T) {
	RegisterTestingT(t)
	ops := RevisionOps{RemoveTasks: []string{"ghost"}}
	detail := preProjectionChecks(ops, []*api.PlanTask{}, map[string]*api.PlanTask{})
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("remove_unknown_task"))
}

func TestPreProjectionChecks_UpdateInProgressOnlyAllowsDescription(t *testing.T) {
	// Description-only updates on a running task are OK (cosmetic
	// note for the human reading the plan). Touching anything else
	// would race with the running execution.
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "running", Status: "in_progress"},
	}
	byName := map[string]*api.PlanTask{"running": existing[0]}

	desc := "clarified instructions"
	descOnlyOps := RevisionOps{UpdateTasks: []RevisionUpdate{
		{Name: "running", Description: &desc},
	}}
	Expect(preProjectionChecks(descOnlyOps, existing, byName)).To(BeNil())

	flowID := "f1"
	withFlowOps := RevisionOps{UpdateTasks: []RevisionUpdate{
		{Name: "running", FlowID: &flowID},
	}}
	detail := preProjectionChecks(withFlowOps, existing, byName)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cannot_update_non_pending_task"))
}

func TestPreProjectionChecks_AddCollideRejected(t *testing.T) {
	// Adding a task with the same name as an existing one is a
	// collision UNLESS the existing one is also being removed in
	// the same batch (legitimate swap pattern).
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "step1", Status: "pending"},
	}
	byName := map[string]*api.PlanTask{"step1": existing[0]}

	colliding := RevisionOps{AddTasks: []RevisionTask{
		{Name: "step1"},
	}}
	detail := preProjectionChecks(colliding, existing, byName)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("add_collides_with_existing"))
}

func TestPreProjectionChecks_AddSwapAllowed(t *testing.T) {
	// Remove "step1" AND add "step1" — that's a swap, allowed.
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "step1", Status: "failed"},
	}
	byName := map[string]*api.PlanTask{"step1": existing[0]}

	swap := RevisionOps{
		RemoveTasks: []string{"step1"},
		AddTasks:    []RevisionTask{{Name: "step1"}},
	}
	Expect(preProjectionChecks(swap, existing, byName)).To(BeNil())
}

// === Projection validator ===

func TestValidateProjection_DetectsCycle(t *testing.T) {
	RegisterTestingT(t)
	projected := []*api.PlanTask{
		{Name: "a", DependsOn: pq.StringArray{"b"}},
		{Name: "b", DependsOn: pq.StringArray{"a"}},
	}
	detail := validateProjection(projected)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("cycle"))
}

func TestValidateProjection_UnknownDependency(t *testing.T) {
	RegisterTestingT(t)
	projected := []*api.PlanTask{
		{Name: "a", DependsOn: pq.StringArray{"nonexistent"}},
	}
	detail := validateProjection(projected)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("unknown_dependency"))
}

func TestValidateProjection_SelfDependency(t *testing.T) {
	RegisterTestingT(t)
	projected := []*api.PlanTask{
		{Name: "loop", DependsOn: pq.StringArray{"loop"}},
	}
	detail := validateProjection(projected)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("self_dependency"))
}

func TestValidateProjection_PartialFlowRefRejected(t *testing.T) {
	RegisterTestingT(t)
	flowID := "f1"
	projected := []*api.PlanTask{
		{Name: "partial", FlowID: &flowID},
	}
	detail := validateProjection(projected)
	Expect(detail).NotTo(BeNil())
	Expect(detail["reason"]).To(Equal("partial_flow_ref"))
}

func TestValidateProjection_LinearChainAccepted(t *testing.T) {
	// Sanity: a→b→c with no cycles or unknown refs validates.
	RegisterTestingT(t)
	projected := []*api.PlanTask{
		{Name: "a"},
		{Name: "b", DependsOn: pq.StringArray{"a"}},
		{Name: "c", DependsOn: pq.StringArray{"b"}},
	}
	Expect(validateProjection(projected)).To(BeNil())
}

// === Projection logic ===

func TestProjectPostRevise_AddTasksGetUUIDs(t *testing.T) {
	// Each added task receives a fresh UUID before persistence so
	// other adds in the same batch can reference it by name.
	RegisterTestingT(t)
	existing := []*api.PlanTask{}
	ops := RevisionOps{
		AddTasks: []RevisionTask{{Name: "x"}, {Name: "y"}},
	}
	projected, addedMap := projectPostRevise(existing, ops)
	Expect(projected).To(HaveLen(2))
	Expect(addedMap).To(HaveLen(2))
	Expect(addedMap["x"]).NotTo(BeEmpty())
	Expect(addedMap["y"]).NotTo(BeEmpty())
	Expect(addedMap["x"]).NotTo(Equal(addedMap["y"]))
}

func TestProjectPostRevise_RemovesAreDropped(t *testing.T) {
	RegisterTestingT(t)
	existing := []*api.PlanTask{
		{ID: "t1", Name: "keep", Status: "pending"},
		{ID: "t2", Name: "drop", Status: "pending"},
	}
	ops := RevisionOps{RemoveTasks: []string{"drop"}}
	projected, _ := projectPostRevise(existing, ops)
	Expect(projected).To(HaveLen(1))
	Expect(projected[0].Name).To(Equal("keep"))
}

func TestProjectPostRevise_UpdatesAreApplied(t *testing.T) {
	RegisterTestingT(t)
	originalDesc := "old"
	newDesc := "new"
	existing := []*api.PlanTask{
		{ID: "t1", Name: "task", Status: "pending", Description: &originalDesc},
	}
	ops := RevisionOps{UpdateTasks: []RevisionUpdate{
		{Name: "task", Description: &newDesc},
	}}
	projected, _ := projectPostRevise(existing, ops)
	Expect(projected).To(HaveLen(1))
	Expect(*projected[0].Description).To(Equal("new"))
	// Source struct must NOT be mutated.
	Expect(*existing[0].Description).To(Equal("old"))
}
