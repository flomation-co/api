// Package poller contains background polling goroutines migrated from
// Launch to the API. These pollers previously made HTTP calls from Launch
// back to the API; now they use direct persistence calls, eliminating
// network round-trips and simplifying the architecture.
package poller

import (
	"encoding/json"
	"time"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// RetentionPersistence defines the DB methods the retention poller needs.
type RetentionPersistence interface {
	GetAgentsWithRetentionPolicy() ([]struct {
		ID                  string `db:"id"`
		MemoryRetentionDays int    `db:"memory_retention_days"`
	}, error)
	DeleteExpiredMemories(limit int) (int64, error)
	DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error)
	CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error)
}

// RetentionPoller enforces per-agent memory retention policies and
// deletes individually expired memories. Runs every hour.
type RetentionPoller struct {
	persistence RetentionPersistence
}

// StartRetentionPoller creates and starts the retention poller goroutine.
func StartRetentionPoller(p RetentionPersistence) *RetentionPoller {
	rp := &RetentionPoller{persistence: p}
	go rp.watch()
	return rp
}

func (rp *RetentionPoller) watch() {
	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	log.Info("retention poller started (API-side, 1h interval)")
	rp.poll()

	for range ticker.C {
		rp.poll()
	}
}

func (rp *RetentionPoller) poll() {
	// Step 1: delete individually expired memories.
	deleted, err := rp.persistence.DeleteExpiredMemories(500)
	if err != nil {
		log.WithError(err).Warn("retention: failed to delete expired memories")
	} else if deleted > 0 {
		log.WithField("count", deleted).Info("retention: deleted expired memories")
		rp.writeAuditLog("", "retention_sweep", deleted)
	}

	// Step 2: enforce per-agent retention policies.
	policies, err := rp.persistence.GetAgentsWithRetentionPolicy()
	if err != nil {
		log.WithError(err).Warn("retention: failed to fetch policies")
		return
	}

	for _, p := range policies {
		olderThan := time.Now().Add(-time.Duration(p.MemoryRetentionDays) * 24 * time.Hour)
		count, err := rp.persistence.DeleteMemoriesOlderThan(p.ID, olderThan, true)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id": p.ID,
				"error":    err,
			}).Warn("retention: failed to enforce policy")
			continue
		}
		if count > 0 {
			log.WithFields(log.Fields{
				"agent_id":       p.ID,
				"retention_days": p.MemoryRetentionDays,
				"deleted":        count,
			}).Info("retention: enforced policy")
			rp.writeAuditLog(p.ID, "retention_sweep", count)
		}
	}
}

func (rp *RetentionPoller) writeAuditLog(agentID, eventType string, count int64) {
	detail, _ := json.Marshal(map[string]interface{}{
		"deleted_count": count,
	})
	actorID := "retention_poller"
	_, _ = rp.persistence.CreateAuditLogEntry(api.AgentAuditLog{
		AgentID:      agentID,
		ActorType:    "retention",
		ActorID:      &actorID,
		EventType:    eventType,
		ResourceType: "memory",
		Detail:       json.RawMessage(detail),
	})
}