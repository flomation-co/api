package persistence

// Plan cancel — user- or AI-initiated halt of a plan. Symmetric
// with the writeback (plan_writeback.go) and block (plan_block.go)
// transactions: lock → transition → audit → poke → publish-post-
// commit. The differences are:
//
//   * Cancel transitions the WHOLE plan to terminal, not just one
//     task. All pending + in_progress tasks cascade to status
//     'cancelled' in a single UPDATE.
//   * Cancel is initiated externally (editor button or AI tool),
//     not from inside a runner writeback or AI tool loop inside a
//     plan task.
//   * Cancel events fan out one plan_cancelled + one task_cancelled
//     per cascaded task. SSE subscribers see the plan-level event
//     plus per-task transitions so the editor's task list
//     auto-updates row-by-row.
//
// Idempotency: a plan that's already terminal (completed,
// cancelled, or blocked) returns CancelOutcomeIdempotent with no
// events. Blocked is intentionally non-cancellable here — see the
// note below; cancellation of blocked plans is allowed at the HTTP
// layer via a separate UI path if/when needed in M4.
//
// Actually — re-reading M3 scope: blocked plans ARE cancellable
// from the UI (a stuck plan is exactly the kind the user wants to
// stop). Treat blocked the same as active for cancel eligibility.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"flomation.app/automate/api"
)

// CancelOutcome reports what happened. NotFound and Idempotent let
// the HTTP handler distinguish "row gone" from "already terminal"
// — both are non-error states the caller can surface to the user
// (or AI) as a clean response without retry pressure.
type CancelOutcome string

const (
	CancelOutcomeCancelled  CancelOutcome = "cancelled"
	CancelOutcomeNotFound   CancelOutcome = "not_found"
	CancelOutcomeIdempotent CancelOutcome = "idempotent"
)

// CancelPlan halts a plan: transitions the plan row to
// status='cancelled', cascades all pending + in_progress tasks to
// 'cancelled', writes plan_cancelled + per-task task_cancelled
// audit events, and publishes them to SSE subscribers post-commit.
//
// Idempotent: a plan already in a terminal state (completed or
// cancelled) returns CancelOutcomeIdempotent with no events.
// Blocked plans ARE cancellable — they're stuck, and the whole
// point of cancel is to unstick them.
//
// reason is stored verbatim in plan.cancelled_reason. Pass "" if
// the caller didn't provide one (the user might not type anything
// in the confirmation dialog).
func (s *Service) CancelPlan(ctx context.Context, planID, reason string) (CancelOutcome, error) {
	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return CancelOutcomeNotFound, fmt.Errorf("begin cancel tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := cancelGetPlanForUpdate(ctx, tx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CancelOutcomeNotFound, nil
		}
		return CancelOutcomeNotFound, fmt.Errorf("lock plan: %w", err)
	}

	// Idempotency: completed and cancelled are terminal. Blocked is
	// NOT — see the file header for the reasoning.
	if plan.Status == "completed" || plan.Status == "cancelled" {
		return CancelOutcomeIdempotent, nil
	}

	cancelledReason := truncateReason(reason)
	now := time.Now()

	// 1. Transition the plan row. next_check_at = NULL is
	// defence-in-depth — isPlanTerminal already keeps the poller
	// from acting on cancelled plans, but nulling the column means
	// even a poller bug can't re-dispatch this plan.
	if _, err := tx.ExecContext(ctx, `
		UPDATE plan
		SET status = 'cancelled',
		    cancelled_at = $2,
		    cancelled_reason = $3,
		    next_check_at = NULL,
		    updated_at = NOW()
		WHERE id = $1`, plan.ID, now, cancelledReason); err != nil {
		return CancelOutcomeNotFound, fmt.Errorf("transition plan: %w", err)
	}

	// 2. Cascade tasks. Single UPDATE … RETURNING id covers all
	// affected rows; no row-by-row processing needed. The writeback
	// path's idempotency guard means a runner completing a task
	// AFTER we transition is a no-op against the cancelled row
	// (writeback only transitions from in_progress).
	var cancelledTaskIDs []string
	if err := tx.SelectContext(ctx, &cancelledTaskIDs, `
		UPDATE plan_task
		SET status = 'cancelled',
		    completed_at = $2,
		    updated_at = NOW()
		WHERE plan_id = $1
		  AND status IN ('pending', 'in_progress')
		RETURNING id`, plan.ID, now); err != nil {
		return CancelOutcomeCancelled, fmt.Errorf("cascade tasks: %w", err)
	}

	// 3. Audit. One plan_cancelled event + one task_cancelled per
	// cascaded task. Both surface to the editor's SSE listener and
	// the timeline view.
	planEventData, _ := json.Marshal(map[string]interface{}{
		"reason":          cancelledReason,
		"task_count":      len(cancelledTaskIDs),
	})
	planEvent, err := tickInsertPlanEvent(ctx, tx, plan.ID, nil, "plan_cancelled", planEventData)
	if err != nil {
		return CancelOutcomeCancelled, fmt.Errorf("audit plan_cancelled: %w", err)
	}

	pendingEvents := []*api.PlanEvent{planEvent}
	for _, taskID := range cancelledTaskIDs {
		// Closure assignment: SQL UPDATE doesn't let us thread the
		// reason per-task in this single statement, so the
		// task_cancelled event carries the plan's reason.
		taskID := taskID
		taskEventData, _ := json.Marshal(map[string]interface{}{
			"reason": cancelledReason,
		})
		ev, evErr := tickInsertPlanEvent(ctx, tx, plan.ID, &taskID, "task_cancelled", taskEventData)
		if evErr != nil {
			return CancelOutcomeCancelled, fmt.Errorf("audit task_cancelled: %w", evErr)
		}
		pendingEvents = append(pendingEvents, ev)
	}

	if err := tx.Commit(); err != nil {
		return CancelOutcomeCancelled, fmt.Errorf("commit cancel: %w", err)
	}

	// Tx committed — publish to SSE subscribers. No-op when no
	// listener is wired (background poller startup before HTTP
	// service init).
	s.publishPlanEvents(pendingEvents)

	return CancelOutcomeCancelled, nil
}

// cancelGetPlanForUpdate locks the plan row for the duration of
// the cancel transaction. SELECT … FOR UPDATE rather than
// FOR UPDATE SKIP LOCKED — cancel must wait for any in-flight
// tick to finish rather than yielding to it, otherwise the user
// could click cancel and have the tick spawn a new in_progress
// task immediately after.
func cancelGetPlanForUpdate(ctx context.Context, tx *sqlx.Tx, planID string) (*api.Plan, error) {
	var plan api.Plan
	err := tx.GetContext(ctx, &plan,
		`SELECT * FROM plan WHERE id = $1 FOR UPDATE`, planID)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

// truncateReason keeps cancelled_reason readable. Mirrors the
// truncateError helper in plan_writeback.go — same cap, same
// suffix. Empty input returns empty output (NOT empty quotes).
func truncateReason(reason string) string {
	const max = 2048
	if len(reason) <= max {
		return reason
	}
	return reason[:max] + "… [truncated]"
}
