package http

import (
	"sync"
)

// LogHub manages in-memory pub/sub for execution log streaming.
// Runners publish log lines; SSE clients subscribe to receive them.
type LogHub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan string
	buffers     map[string][]string
}

func NewLogHub() *LogHub {
	return &LogHub{
		subscribers: make(map[string][]chan string),
		buffers:     make(map[string][]string),
	}
}

// Publish sends log lines to all subscribers for the given execution ID
// and appends them to the buffer for late joiners.
func (h *LogHub) Publish(executionID string, lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buffers[executionID] = append(h.buffers[executionID], lines...)

	for _, ch := range h.subscribers[executionID] {
		for _, line := range lines {
			select {
			case ch <- line:
			default:
				// Subscriber too slow, drop the line
			}
		}
	}
}

// Subscribe returns a channel that receives log lines for the given execution.
// It also replays any buffered lines. Call Unsubscribe when done.
func (h *LogHub) Subscribe(executionID string) (chan string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan string, 256)
	h.subscribers[executionID] = append(h.subscribers[executionID], ch)

	buffered := make([]string, len(h.buffers[executionID]))
	copy(buffered, h.buffers[executionID])

	return ch, buffered
}

// Unsubscribe removes a subscriber channel.
func (h *LogHub) Unsubscribe(executionID string, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subs := h.subscribers[executionID]
	for i, s := range subs {
		if s == ch {
			h.subscribers[executionID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}

	close(ch)
}

// Complete publishes a completion sentinel and cleans up after a delay.
func (h *LogHub) Complete(executionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ch := range h.subscribers[executionID] {
		select {
		case ch <- "__COMPLETE__":
		default:
		}
	}
}

// Cleanup removes all state for an execution.
func (h *LogHub) Cleanup(executionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ch := range h.subscribers[executionID] {
		close(ch)
	}

	delete(h.subscribers, executionID)
	delete(h.buffers, executionID)
}
