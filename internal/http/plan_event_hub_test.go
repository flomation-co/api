package http

// Unit tests for PlanEventHub. Mirrors the AgentSessionHub
// behavioural contract: subscribe → publish → channel receives;
// unsubscribe closes; slow subscribers drop instead of blocking.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func TestPlanEventHub_SubscribePublishReceive(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	ch := hub.Subscribe("plan-1")

	ev := &api.PlanEvent{
		PlanID:    "plan-1",
		EventType: "task_started",
		Data:      json.RawMessage(`{"execution_id":"exec-1"}`),
	}
	hub.Publish(ev)

	select {
	case envelope := <-ch:
		Expect(envelope.Type).To(Equal("task_started"))
		Expect(envelope.Data).To(Equal(ev))
	case <-time.After(time.Second):
		t.Fatal("expected event on channel within 1s")
	}
}

func TestPlanEventHub_PublishToWrongPlan_NotReceived(t *testing.T) {
	// Subscribers should only receive events for THEIR planID. A
	// publish on another planID must NOT leak across.
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	ch := hub.Subscribe("plan-1")

	hub.Publish(&api.PlanEvent{PlanID: "plan-2", EventType: "task_started"})

	select {
	case <-ch:
		t.Fatal("received cross-plan event")
	case <-time.After(50 * time.Millisecond):
		// Pass — no event leaked through.
	}
}

func TestPlanEventHub_Unsubscribe_ClosesChannel(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	ch := hub.Subscribe("plan-1")
	hub.Unsubscribe("plan-1", ch)

	// A closed channel returns its zero value with ok=false on receive.
	select {
	case _, ok := <-ch:
		Expect(ok).To(BeFalse())
	case <-time.After(time.Second):
		t.Fatal("expected channel close within 1s")
	}
}

func TestPlanEventHub_SlowSubscriber_Drops(t *testing.T) {
	// A subscriber that never reads should not block publishers.
	// Channel buffer is 64; publish 200 and confirm the publisher
	// returns without hanging.
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	_ = hub.Subscribe("plan-1") // deliberately never read

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			hub.Publish(&api.PlanEvent{PlanID: "plan-1", EventType: "task_started"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Pass — publisher didn't block.
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked on slow subscriber")
	}
}

func TestPlanEventHub_MultipleSubscribers_AllReceive(t *testing.T) {
	// Two subscribers on the same planID should both receive each
	// published event.
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	chA := hub.Subscribe("plan-1")
	chB := hub.Subscribe("plan-1")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-chA
	}()
	go func() {
		defer wg.Done()
		<-chB
	}()

	hub.Publish(&api.PlanEvent{PlanID: "plan-1", EventType: "task_started"})

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("not all subscribers received the event")
	}
}

func TestPlanEventHub_NilEvent_NoOp(t *testing.T) {
	// Defensive: a nil event must be a no-op rather than a nil-
	// pointer panic. The publish path is called from inside
	// persistence helpers; a nil slipping through must not crash.
	t.Parallel()
	RegisterTestingT(t)

	hub := NewPlanEventHub()
	ch := hub.Subscribe("plan-1")

	hub.Publish(nil) // must not panic

	select {
	case <-ch:
		t.Fatal("received an event for nil publish")
	case <-time.After(50 * time.Millisecond):
		// Pass.
	}
}

func TestPlanEventEnvelope_MarshalSSE(t *testing.T) {
	// The SSE body must be the JSON-encoded PlanEvent. The editor
	// will JSON.parse() this to read the timeline.
	t.Parallel()
	RegisterTestingT(t)

	ev := &api.PlanEvent{
		ID:        42,
		PlanID:    "plan-1",
		EventType: "plan_completed",
		Data:      json.RawMessage(`{}`),
	}
	envelope := PlanEventEnvelope{Type: ev.EventType, Data: ev}

	out := envelope.MarshalSSE()
	Expect(out).To(ContainSubstring(`"plan_id":"plan-1"`))
	Expect(out).To(ContainSubstring(`"event_type":"plan_completed"`))
}
