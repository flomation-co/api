package persistence

// Pure-function tests for the tick logic. The transactional dispatch
// path requires a live Postgres and is covered end-to-end in M1
// commit 9 + local manual validation. What we pin here is the status
// derivation, which is the load-bearing decision the tick makes.

import (
	"testing"

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
