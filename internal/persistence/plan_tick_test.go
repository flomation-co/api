package persistence

// Pure-function tests for the tick logic. The transactional dispatch
// path requires a live Postgres and is covered end-to-end in M1
// commit 9 + local manual validation. What we pin here is the status
// derivation, which is the load-bearing decision the tick makes.

import (
	"encoding/json"
	"testing"

	api "flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func TestDerivePlanStatus(t *testing.T) {
	cases := []struct {
		name    string
		current string
		counts  map[string]int
		want    string
	}{
		{
			name:    "pending tasks keep plan active",
			current: "active",
			counts:  map[string]int{"pending": 1, "completed": 2},
			want:    "active",
		},
		{
			name:    "in_progress alone keeps plan active",
			current: "active",
			counts:  map[string]int{"in_progress": 1},
			want:    "active",
		},
		{
			name:    "all tasks completed → completed",
			current: "active",
			counts:  map[string]int{"completed": 3},
			want:    "completed",
		},
		{
			name:    "completed mix with failed and no active → blocked",
			current: "active",
			counts:  map[string]int{"completed": 1, "failed": 1},
			want:    "blocked",
		},
		{
			name:    "only cancelled → blocked",
			current: "active",
			counts:  map[string]int{"cancelled": 1},
			want:    "blocked",
		},
		{
			name:    "empty counts (defensive) → current",
			current: "active",
			counts:  map[string]int{},
			want:    "active",
		},
		{
			name:    "pending + failed → still active (recovery via retry possible)",
			current: "active",
			counts:  map[string]int{"pending": 1, "failed": 1},
			want:    "active",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			RegisterTestingT(t)
			Expect(derivePlanStatus(c.current, c.counts)).To(Equal(c.want))
		})
	}
}

func TestIsPlanTerminal(t *testing.T) {
	RegisterTestingT(t)
	Expect(isPlanTerminal("completed")).To(BeTrue())
	Expect(isPlanTerminal("cancelled")).To(BeTrue())
	Expect(isPlanTerminal("active")).To(BeFalse())
	Expect(isPlanTerminal("blocked")).To(BeFalse())
	Expect(isPlanTerminal("draft")).To(BeFalse())
}

// TestTickConstants_StayStable ensures MaxConcurrentPlanTasks and
// GlobalAttemptCap don't drift accidentally. Changes require an
// intentional update to this test + a thought about quota impact.
func TestTickConstants_StayStable(t *testing.T) {
	RegisterTestingT(t)
	Expect(MaxConcurrentPlanTasks).To(Equal(5))
	Expect(GlobalAttemptCap).To(Equal(10))
}

// === M1.5 commit 3: orchestrator dispatch helpers ===

func TestBuildPlanTaskPrompt_FullShape(t *testing.T) {
	RegisterTestingT(t)
	desc := "Pull this quarter's revenue and churn"
	task := &api.PlanTask{
		Name:        "pull_metrics",
		Description: &desc,
	}
	plan := &api.Plan{Title: "Q3 review"}
	inputs := json.RawMessage(`{"quarter":"Q3"}`)
	upstream := map[string]map[string]interface{}{
		"prior_task": {"rows": float64(42)},
	}
	got := buildPlanTaskPrompt(plan, task, inputs, upstream)

	Expect(got).To(ContainSubstring("Progress plan task 'pull_metrics'"))
	Expect(got).To(ContainSubstring("plan 'Q3 review'"))
	Expect(got).To(ContainSubstring("Description: Pull this quarter's"))
	Expect(got).To(ContainSubstring(`Inputs: {"quarter":"Q3"}`))
	Expect(got).To(ContainSubstring(`Upstream outputs: {"prior_task":`))
	// Terminator hint is load-bearing — without it the AI doesn't
	// know which tool to call to end the execution.
	Expect(got).To(ContainSubstring("set_output"))
	Expect(got).To(ContainSubstring("plan/block"))
}

func TestBuildPlanTaskPrompt_OmitsEmptySections(t *testing.T) {
	// No description, no inputs, no upstream — the prompt is still
	// coherent. Used by the simplest "do this one thing" task shape.
	RegisterTestingT(t)
	task := &api.PlanTask{Name: "step", Description: nil}
	plan := &api.Plan{Title: "Demo"}
	got := buildPlanTaskPrompt(plan, task, json.RawMessage("{}"), nil)
	Expect(got).To(ContainSubstring("Progress plan task 'step'"))
	Expect(got).NotTo(ContainSubstring("Description:"))
	Expect(got).NotTo(ContainSubstring("Inputs:"))
	Expect(got).NotTo(ContainSubstring("Upstream outputs:"))
	Expect(got).To(ContainSubstring("set_output"))
}

func TestDecodeInputsForTriggerData(t *testing.T) {
	RegisterTestingT(t)
	// Empty / null → empty map (never nil — the AI sees an
	// inspectable object).
	Expect(decodeInputsForTriggerData(nil)).To(Equal(map[string]interface{}{}))
	Expect(decodeInputsForTriggerData(json.RawMessage(""))).To(Equal(map[string]interface{}{}))
	Expect(decodeInputsForTriggerData(json.RawMessage("null"))).To(Equal(map[string]interface{}{}))

	// Malformed → empty map (defensive — defunct payload won't crash dispatch).
	Expect(decodeInputsForTriggerData(json.RawMessage(`{broken`))).To(Equal(map[string]interface{}{}))

	// Valid round-trip.
	got := decodeInputsForTriggerData(json.RawMessage(`{"k":"v","n":42}`))
	Expect(got).To(HaveKeyWithValue("k", "v"))
	Expect(got).To(HaveKeyWithValue("n", float64(42)))
}

func TestUpstreamSafeForTriggerData(t *testing.T) {
	RegisterTestingT(t)
	Expect(upstreamSafeForTriggerData(nil)).To(Equal(map[string]map[string]interface{}{}))
	in := map[string]map[string]interface{}{"t": {"x": "y"}}
	Expect(upstreamSafeForTriggerData(in)).To(Equal(in))
}

func TestStrDerefOrEmpty(t *testing.T) {
	RegisterTestingT(t)
	Expect(strDerefOrEmpty(nil)).To(Equal(""))
	v := "hi"
	Expect(strDerefOrEmpty(&v)).To(Equal("hi"))
}
