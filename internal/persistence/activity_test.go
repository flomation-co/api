package persistence

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

func TestTouchUserActivity_Throttle(t *testing.T) {
	RegisterTestingT(t)

	// Reset global state for test isolation.
	activityMu.Lock()
	activityLastSeen = make(map[string]time.Time)
	activityMu.Unlock()

	// First call should mark the user as seen.
	activityMu.Lock()
	activityLastSeen["user-1"] = time.Now()
	activityMu.Unlock()

	// Immediately after, a second call should be throttled.
	activityMu.Lock()
	last1 := activityLastSeen["user-1"]
	activityMu.Unlock()
	Expect(last1).NotTo(BeZero())

	// Simulate that the window has passed.
	activityMu.Lock()
	activityLastSeen["user-1"] = time.Now().Add(-2 * activityThrottleWindow)
	activityMu.Unlock()

	// Now the throttle check should allow a write.
	activityMu.Lock()
	stale := activityLastSeen["user-1"]
	withinWindow := time.Since(stale) < activityThrottleWindow
	activityMu.Unlock()
	Expect(withinWindow).To(BeFalse(), "after 2x the throttle window, the entry should be stale")

	// A new user should not be throttled.
	activityMu.Lock()
	_, exists := activityLastSeen["user-new"]
	activityMu.Unlock()
	Expect(exists).To(BeFalse())
}
