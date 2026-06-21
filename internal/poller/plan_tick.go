package poller

// Plan tick poller — the fallback driver for the Agent Planning M1
// orchestrator. See plans/agent_planning_m1.md commit 8 and the
// architecture note explaining why this lives API-side rather than
// in Launch.
//
// Three reactive wake-ups already cover the responsive cases:
//
//   1. Plan creation sets next_check_at = NOW (commit 4).
//   2. Task completion writeback sets next_check_at = NOW (commit 6).
//   3. TickPlan itself sets next_check_at to the earliest pending
//      not_before so time-gated tasks don't get re-ticked until they
//      can fire (commit 5).
//
// This poller catches the FOURTH case — time-gated tasks whose
// not_before passes between reactive wake-ups, and the
// belt-and-braces sweep for any reactive wake-up that didn't fire
// (e.g. a writeback that crashed mid-transaction). 30s is the right
// cadence: fast enough that a time-gated task starts within ~30s of
// being due, slow enough that the partial-index scan is invisible.

import (
	"context"
	"errors"
	"time"

	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// PlanTickPersistence is the narrow slice of the persistence service
// the poller touches. Kept tight so the poller tests don't need to
// import the full Service type.
type PlanTickPersistence interface {
	ListReadyPlanIDs(limit int) ([]string, error)
	TickPlan(ctx context.Context, planID string) (*persistence.TickPlanResult, error)
}

// PlanTickPoller is the goroutine struct. Held as a return value
// from StartPlanTickPoller mainly so tests can poke `.poll()`
// directly without waiting for the ticker.
type PlanTickPoller struct {
	persistence PlanTickPersistence
}

const (
	planTickInterval  = 30 * time.Second
	planTickBatchSize = 50
	planTickTimeout   = 15 * time.Second // per-plan upper bound
)

// StartPlanTickPoller spins up the sweeper goroutine.
func StartPlanTickPoller(p PlanTickPersistence) *PlanTickPoller {
	pp := &PlanTickPoller{persistence: p}
	go pp.watch()
	return pp
}

func (pp *PlanTickPoller) watch() {
	// Stagger relative to the other pollers so a cold start doesn't
	// have them all hammering the DB on the same second.
	time.Sleep(20 * time.Second)

	ticker := time.NewTicker(planTickInterval)
	defer ticker.Stop()

	log.Info("plan tick poller started (API-side, 30s interval)")
	pp.poll()
	for range ticker.C {
		pp.poll()
	}
}

// poll runs one scan cycle: fetch up to planTickBatchSize ready plans
// and tick each. Per-plan failures are logged but do NOT abort the
// batch — one stuck plan shouldn't starve its siblings.
func (pp *PlanTickPoller) poll() {
	ids, err := pp.persistence.ListReadyPlanIDs(planTickBatchSize)
	if err != nil {
		log.WithError(err).Warn("plan tick: list ready plans failed")
		return
	}
	if len(ids) == 0 {
		return
	}

	var ticked, locked, terminal, fired int
	for _, planID := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), planTickTimeout)
		result, err := pp.persistence.TickPlan(ctx, planID)
		cancel()

		switch {
		case err == nil:
			ticked++
			fired += len(result.Fired)
		case errors.Is(err, persistence.ErrPlanTerminal):
			// Plan transitioned to terminal between ListReady and Tick
			// (e.g. cancelled via API). Normal race, not an error.
			terminal++
		case errors.Is(err, persistence.ErrPlanLocked):
			// Another instance is ticking it right now — the lease
			// pattern (M3) would prevent this entirely, but for M1 we
			// rely on FOR UPDATE SKIP LOCKED + this graceful skip.
			locked++
		default:
			log.WithFields(log.Fields{
				"plan_id": planID,
				"error":   err,
			}).Warn("plan tick: per-plan tick failed")
		}
	}

	if ticked > 0 || fired > 0 || locked > 0 || terminal > 0 {
		log.WithFields(log.Fields{
			"scanned":  len(ids),
			"ticked":   ticked,
			"fired":    fired,
			"locked":   locked,
			"terminal": terminal,
		}).Info("plan tick: scan complete")
	}
}
