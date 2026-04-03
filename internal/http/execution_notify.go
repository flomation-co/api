package http

import "sync"

// ExecutionNotifier provides a pub-sub mechanism for notifying runners
// that new executions are available. Used to implement long-polling on
// the runner's check-for-work endpoint.
type ExecutionNotifier struct {
	mu       sync.Mutex
	channels map[string][]chan struct{} // key → list of waiting channels
}

// NewExecutionNotifier creates a new notifier.
func NewExecutionNotifier() *ExecutionNotifier {
	return &ExecutionNotifier{
		channels: make(map[string][]chan struct{}),
	}
}

// Wait registers interest in new executions and returns a channel that
// will be closed when a notification arrives. The key can be an
// organisation ID, queue ID, or "" for the global queue.
func (n *ExecutionNotifier) Wait(key string) <-chan struct{} {
	ch := make(chan struct{})

	n.mu.Lock()
	n.channels[key] = append(n.channels[key], ch)
	n.mu.Unlock()

	return ch
}

// Notify signals all waiters for the given key (and the global ""
// key) that new work is available.
func (n *ExecutionNotifier) Notify(keys ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Always notify the global key
	allKeys := append(keys, "")

	for _, key := range allKeys {
		waiters := n.channels[key]
		for _, ch := range waiters {
			select {
			case <-ch:
				// already closed
			default:
				close(ch)
			}
		}
		delete(n.channels, key)
	}
}
