package persistence

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const activityThrottleWindow = 1 * time.Minute

var (
	activityMu       sync.Mutex
	activityLastSeen = make(map[string]time.Time)
)

// TouchUserActivity updates the last_activity_at timestamp for a user.
// Writes are throttled to at most once per minute per user to avoid
// excessive database writes on high-frequency API calls.
func (s *Service) TouchUserActivity(userID string) {
	now := time.Now()

	activityMu.Lock()
	last, ok := activityLastSeen[userID]
	if ok && now.Sub(last) < activityThrottleWindow {
		activityMu.Unlock()
		return
	}
	activityLastSeen[userID] = now
	activityMu.Unlock()

	_, err := s.conn.Exec(
		`UPDATE users SET last_activity_at = $1 WHERE id = $2`,
		now.UTC(), userID,
	)
	if err != nil {
		log.WithFields(log.Fields{
			"user_id": userID,
			"error":   err,
		}).Warn("failed to update user last_activity_at")
	}
}
