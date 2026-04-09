package persistence

// Execution reaper: periodically cancels stuck and stale executions.
//
// Two classes of problem:
//
//  1. Zombie "running" executions — the runner crashed or restarted
//     mid-execution. The DB still says running but nothing is processing
//     it. These block the system flow concurrency cap and waste a slot.
//
//  2. Stale "created" executions — queued but never claimed. Old
//     extraction jobs for messages from 30 minutes ago aren't useful
//     anymore and just clog the queue.
//
// Runs every 60 seconds as a background goroutine in the API process.

import (
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	reaperInterval = 60 * time.Second
	// Executions stuck in "running" longer than this are assumed dead.
	zombieThreshold = 60 * time.Minute
	// System flow executions stuck in "created" longer than this are
	// stale and should be cancelled. Agent flows get a longer window
	// since they might be legitimately queued behind other work.
	staleSystemFlowThreshold = 10 * time.Minute
)

// StartExecutionReaper launches the background reaper goroutine.
func (s *Service) StartExecutionReaper() {
	go func() {
		time.Sleep(30 * time.Second) // initial delay
		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		log.Info("execution reaper started (60s interval)")

		for range ticker.C {
			s.reapZombies()
			s.reapStaleSystemFlows()
		}
	}()
}

func (s *Service) reapZombies() {
	result, err := s.conn.Exec(`
		UPDATE execution
		SET execution_status = 'executed', completion_status = 'timeout'
		WHERE execution_status = 'running'
		AND created_at < NOW() - $1::interval
	`, zombieThreshold.String())
	if err != nil {
		log.WithError(err).Warn("execution reaper: failed to reap zombies")
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.WithFields(log.Fields{
			"count": rows,
		}).Info("execution reaper: cancelled zombie executions")
	}
}

func (s *Service) reapStaleSystemFlows() {
	result, err := s.conn.Exec(`
		UPDATE execution
		SET execution_status = 'executed', completion_status = 'timeout'
		WHERE execution_status = 'created'
		AND flo_id IN (SELECT id FROM flo WHERE system_flow = TRUE)
		AND created_at < NOW() - $1::interval
	`, staleSystemFlowThreshold.String())
	if err != nil {
		log.WithError(err).Warn("execution reaper: failed to reap stale system flows")
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.WithFields(log.Fields{
			"count": rows,
		}).Info("execution reaper: cancelled stale system flow executions")
	}
}
