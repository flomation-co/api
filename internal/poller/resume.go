package poller

import (
	"encoding/json"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/agent"
	log "github.com/sirupsen/logrus"
)

const (
	resumePollInterval = 15 * time.Second
	resumeBatchSize    = 50
)

// ResumePersistence defines the DB methods the resume poller needs. It drives
// GetSuspendedExecutionsForResume — previously unused — so timed suspensions
// (both the Human-in-the-Loop timeout and the plain Wait action) are actually
// resumed when their resume_at arrives.
type ResumePersistence interface {
	GetSuspendedExecutionsForResume(now time.Time, limit int) ([]*api.Execution, error)
	GetExecutionCheckpoint(id string) (json.RawMessage, error)
	SaveExecutionCheckpoint(id string, checkpoint interface{}) error
	SetExecutionResumeData(id string, data json.RawMessage) error
	UpdateExecutionStatus(id, status string) error
	UpdateCompletionStatus(id, status string) error
	ClearResumeAt(id string) error
	MarkHITLTimedOut(requestID string) (bool, error)
}

// ResumePoller wakes suspended executions whose resume_at has passed.
type ResumePoller struct {
	persistence ResumePersistence
	notifier    agent.ExecutionNotifier
}

// StartResumePoller creates and starts the resume poller goroutine.
func StartResumePoller(p ResumePersistence, n agent.ExecutionNotifier) *ResumePoller {
	rp := &ResumePoller{persistence: p, notifier: n}
	go rp.watch()
	return rp
}

func (rp *ResumePoller) watch() {
	time.Sleep(10 * time.Second)

	ticker := time.NewTicker(resumePollInterval)
	defer ticker.Stop()

	log.Info("resume poller started (API-side, 15s interval)")

	for range ticker.C {
		rp.poll()
	}
}

func (rp *ResumePoller) poll() {
	now := time.Now().UTC()
	execs, err := rp.persistence.GetSuspendedExecutionsForResume(now, resumeBatchSize)
	if err != nil {
		log.WithError(err).Warn("resume poller: failed to fetch due executions")
		return
	}
	for _, e := range execs {
		rp.resumeOne(e)
	}
}

func (rp *ResumePoller) resumeOne(e *api.Execution) {
	var resumeData map[string]interface{}

	// Human-in-the-Loop timeout: flip the request to timed_out first. If that
	// fails because it was already answered, the respond handler is driving
	// the resume — leave this one alone.
	if e.ResumeTriggerType != nil && *e.ResumeTriggerType == "hitl_response" {
		requestID := requestIDFromMatch(e.ResumeTriggerMatch)
		if requestID != "" {
			won, err := rp.persistence.MarkHITLTimedOut(requestID)
			if err != nil {
				log.WithError(err).WithField("execution_id", e.ID).Warn("resume poller: failed to time out HITL request")
				return
			}
			if !won {
				// Already answered — a channel response beat the timeout.
				return
			}
		}
		resumeData = map[string]interface{}{
			"await": map[string]interface{}{
				"request_id": requestID,
				"outcome":    "timeout",
			},
		}
	}

	// Inject resume data into the checkpoint (same mechanism the resume
	// endpoint uses) so it reaches the executor untouched.
	if len(resumeData) > 0 {
		if cpRaw, err := rp.persistence.GetExecutionCheckpoint(e.ID); err == nil && len(cpRaw) > 0 {
			var m map[string]interface{}
			if json.Unmarshal(cpRaw, &m) == nil {
				m["resume_data"] = resumeData
				if patched, err := json.Marshal(m); err == nil {
					if err := rp.persistence.SaveExecutionCheckpoint(e.ID, patched); err != nil {
						log.WithError(err).Error("resume poller: failed to patch checkpoint")
					}
				}
			}
		}
		if j, err := json.Marshal(resumeData); err == nil {
			_ = rp.persistence.SetExecutionResumeData(e.ID, j)
		}
	}

	if err := rp.persistence.UpdateExecutionStatus(e.ID, "created"); err != nil {
		log.WithError(err).WithField("execution_id", e.ID).Error("resume poller: failed to re-queue execution")
		return
	}
	_ = rp.persistence.UpdateCompletionStatus(e.ID, "pending")
	_ = rp.persistence.ClearResumeAt(e.ID)
	rp.notifier.Notify("")

	log.WithField("execution_id", e.ID).Info("resume poller: resumed due execution")
}

// requestIDFromMatch pulls the request_id out of an execution's
// resume_trigger_match JSON ({"request_id": "..."}).
func requestIDFromMatch(match *json.RawMessage) string {
	if match == nil || len(*match) == 0 {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(*match, &m) != nil {
		return ""
	}
	if v, ok := m["request_id"].(string); ok {
		return v
	}
	return ""
}
