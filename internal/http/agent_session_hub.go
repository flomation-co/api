package http

import (
	"encoding/json"
	"sync"
)

// AgentSessionHub manages SSE pub/sub for agent session live updates.
type AgentSessionHub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan AgentSessionEvent
}

// AgentSessionEvent is a typed event pushed to session subscribers.
type AgentSessionEvent struct {
	Type string      `json:"type"` // "message", "execution", "status"
	Data interface{} `json:"data"`
}

func NewAgentSessionHub() *AgentSessionHub {
	return &AgentSessionHub{
		subscribers: make(map[string][]chan AgentSessionEvent),
	}
}

// Subscribe returns a channel for receiving session events.
func (h *AgentSessionHub) Subscribe(sessionID string) chan AgentSessionEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan AgentSessionEvent, 64)
	h.subscribers[sessionID] = append(h.subscribers[sessionID], ch)
	return ch
}

// Unsubscribe removes a subscriber.
func (h *AgentSessionHub) Unsubscribe(sessionID string, ch chan AgentSessionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subs := h.subscribers[sessionID]
	for i, s := range subs {
		if s == ch {
			h.subscribers[sessionID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	close(ch)
}

// Publish sends an event to all subscribers for the given session.
func (h *AgentSessionHub) Publish(sessionID string, event AgentSessionEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.subscribers[sessionID] {
		select {
		case ch <- event:
		default:
			// Subscriber too slow
		}
	}
}

// PublishJSON publishes an event with JSON-encoded data.
func (h *AgentSessionHub) PublishJSON(sessionID string, eventType string, data interface{}) {
	h.Publish(sessionID, AgentSessionEvent{
		Type: eventType,
		Data: data,
	})
}

// MarshalEvent returns the SSE-formatted string for an event.
func (e *AgentSessionEvent) MarshalSSE() string {
	b, _ := json.Marshal(e.Data)
	return string(b)
}
