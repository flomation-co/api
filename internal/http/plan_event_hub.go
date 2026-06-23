package http

// Agent Planning M2 — in-process pub/sub for plan_event rows. Mounted
// behind the SSE endpoint at GET /api/v1/agent/:id/plan/:planID/stream
// so the editor's plan-detail page can render task transitions and
// audit events live as they're persisted.
//
// Cloned shape-for-shape from AgentSessionHub. Two intentional
// differences:
//
//   * Subscribe key is planID (not sessionID). All subscribers
//     watching the same plan get every event for that plan.
//   * The hub is *fed* from the persistence layer via a listener
//     function injected at service startup — see Service.New
//     where planEventHub.Publish is bound to persistence.Service's
//     planEventListener field. Persistence calls the listener
//     ONLY after a successful tx.Commit() so a rollback never
//     leaks events.
//
// Channel buffer (64) and drop-on-slow-subscriber semantics match
// AgentSessionHub verbatim. A subscriber that can't keep up loses
// events; the editor's reconnect/initial-fetch flow heals the gap.

import (
	"encoding/json"
	"sync"

	"flomation.app/automate/api"
)

// PlanEventHub manages SSE pub/sub for plan event live updates.
type PlanEventHub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan PlanEventEnvelope
}

// PlanEventEnvelope wraps the persisted PlanEvent with an SSE-typed
// channel. EventType doubles as the SSE `event:` field so the
// editor's EventSource handler can switch on it without parsing the
// data body.
type PlanEventEnvelope struct {
	Type string         `json:"type"`
	Data *api.PlanEvent `json:"data"`
}

func NewPlanEventHub() *PlanEventHub {
	return &PlanEventHub{
		subscribers: make(map[string][]chan PlanEventEnvelope),
	}
}

// Subscribe returns a channel for receiving plan events.
func (h *PlanEventHub) Subscribe(planID string) chan PlanEventEnvelope {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan PlanEventEnvelope, 64)
	h.subscribers[planID] = append(h.subscribers[planID], ch)
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *PlanEventHub) Unsubscribe(planID string, ch chan PlanEventEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subs := h.subscribers[planID]
	for i, s := range subs {
		if s == ch {
			h.subscribers[planID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	close(ch)
}

// Publish sends a wrapped PlanEvent to all subscribers for the given
// plan. Drops on slow-subscriber to keep one stalled client from
// blocking the hub.
func (h *PlanEventHub) Publish(event *api.PlanEvent) {
	if event == nil {
		return
	}
	envelope := PlanEventEnvelope{
		Type: event.EventType,
		Data: event,
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subscribers[event.PlanID] {
		select {
		case ch <- envelope:
		default:
			// Subscriber too slow — drop. The editor's initial fetch
			// + reconnect path will heal the gap on next connect.
		}
	}
}

// MarshalSSE returns the JSON body for the SSE `data:` line.
// PlanEvent already round-trips cleanly through encoding/json (Data
// is json.RawMessage which is passed through verbatim).
func (e *PlanEventEnvelope) MarshalSSE() string {
	b, _ := json.Marshal(e.Data)
	return string(b)
}
