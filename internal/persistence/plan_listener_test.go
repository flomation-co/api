package persistence

// Unit tests for the post-commit publish hook plumbing.
// The transactional SQL behaviour itself is exercised end-to-end
// by the demo runbook (with a real DB); here we pin the
// listener wiring + the publishPlanEvents fanout.

import (
	"testing"

	api "flomation.app/automate/api"

	. "github.com/onsi/gomega"
)

func TestSetPlanEventListener_Wires(t *testing.T) {
	RegisterTestingT(t)
	s := &Service{}
	called := 0
	s.SetPlanEventListener(func(_ *api.PlanEvent) { called++ })

	// Publish path is only safe to call AFTER tx.Commit() in real
	// code, but the helper itself doesn't touch the DB — it just
	// iterates the listener over the slice.
	s.publishPlanEvents([]*api.PlanEvent{
		{PlanID: "p-1", EventType: "task_started"},
		{PlanID: "p-1", EventType: "task_completed"},
	})

	Expect(called).To(Equal(2))
}

func TestPublishPlanEvents_NilListener_NoOp(t *testing.T) {
	// Persistence service starts up before the HTTP service wires
	// the listener (the API-side plan tick poller is registered
	// during service init). Publishing while the listener is nil
	// must not panic — the events simply don't broadcast.
	RegisterTestingT(t)
	s := &Service{} // listener nil

	s.publishPlanEvents([]*api.PlanEvent{
		{PlanID: "p-1", EventType: "task_started"},
	}) // must not panic
}

func TestPublishPlanEvents_NilEntries_Skipped(t *testing.T) {
	// Defensive: a nil event in the slice (e.g. from a partial
	// failure path) must not fire the listener with a nil
	// pointer.
	RegisterTestingT(t)
	s := &Service{}
	calls := 0
	s.SetPlanEventListener(func(ev *api.PlanEvent) {
		calls++
		Expect(ev).NotTo(BeNil()) // listener must never see nil
	})

	s.publishPlanEvents([]*api.PlanEvent{
		nil,
		{PlanID: "p-1", EventType: "task_started"},
		nil,
	})

	Expect(calls).To(Equal(1))
}

func TestPublishPlanEvents_EmptySlice_NoOp(t *testing.T) {
	RegisterTestingT(t)
	s := &Service{}
	calls := 0
	s.SetPlanEventListener(func(_ *api.PlanEvent) { calls++ })

	s.publishPlanEvents(nil)
	s.publishPlanEvents([]*api.PlanEvent{})

	Expect(calls).To(Equal(0))
}
