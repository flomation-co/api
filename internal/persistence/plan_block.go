package persistence

// Plan-task block — the AI-initiated escape hatch for plan tasks
// that genuinely cannot make progress (missing data, ambiguous
// instruction, external dependency unreachable). Symmetric with the
// existing completion writeback (plan_writeback.go) — both transition
// an in_progress plan_task to a terminal state and poke the plan so
// the next tick re-derives plan status. The differences are who
// initiates (AI vs the runner's completion handler) and the recorded
// reason (free-text from the model vs structured completion outcome).
//
// One method, all-or-nothing: lock the task, transition to failed,
// audit, poke the plan. Splitting these into per-step helpers would
// force the HTTP handler to manage a tx for behaviour that is one
// logical unit.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// BlockOutcome reports what happened. NotFound and Idempotent let the
// HTTP handler distinguish "row gone" from "already terminal" — both
// are non-error states the caller can surface to the AI as a clean
// tool_result without retry pressure.
type BlockOutcome string

const (
	BlockOutcomeBlocked    BlockOutcome = "blocked"
	BlockOutcomeNotFound   BlockOutcome = "not_found"
	BlockOutcomeIdempotent BlockOutcome = "idempotent"
)

// BlockPlanTask transitions an in_progress plan_task to status='failed'
// with the supplied reason, writes a task_blocked audit event, and
// pokes the plan's next_check_at so the next orchestrator tick derives
// plan status='blocked'. Returns BlockOutcomeIdempotent when the task
// is already terminal (failed/completed/cancelled) — the AI may call
// plan/block multiple times across retries and we treat repeats as
// no-ops rather than errors so the model doesn't get tripped up by
// transient races with the writeback.
func (s *Service) BlockPlanTask(ctx context.Context, planTaskID, reason string) (BlockOutcome, error) {
	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return BlockOutcomeNotFound, fmt.Errorf("begin block tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := writebackGetTaskForUpdate(ctx, tx, planTaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlockOutcomeNotFound, nil
		}
		return BlockOutcomeNotFound, fmt.Errorf("lock plan_task: %w", err)
	}

	// Idempotency guard: only transition from non-terminal states. The
	// completion writeback (plan_writeback.go) follows the same posture
	// — anything already terminal means another path won. Pending is
	// included here (alongside in_progress) because the AI may call
	// plan/block on the FIRST tool turn before the writeback bumps to
	// completed — there's no architectural reason to forbid early
	// blocks.
	if task.Status != "pending" && task.Status != "in_progress" {
		return BlockOutcomeIdempotent, nil
	}

	blockedReason := truncateError(reason)
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE plan_task
		SET status = 'failed',
		    last_error = $2,
		    completed_at = $3,
		    updated_at = NOW()
		WHERE id = $1`, task.ID, blockedReason, now); err != nil {
		return BlockOutcomeNotFound, fmt.Errorf("transition plan_task: %w", err)
	}

	eventData, _ := json.Marshal(map[string]interface{}{
		"reason": blockedReason,
	})
	if err := tickInsertPlanEvent(ctx, tx, task.PlanID, &task.ID, "task_blocked", eventData); err != nil {
		return BlockOutcomeBlocked, fmt.Errorf("audit task_blocked: %w", err)
	}

	// Poke the plan so the orchestrator tick re-derives status on the
	// next pass. The tick's derivePlanStatus sees a 'failed' task with
	// no remaining retryable attempts and flips the plan to 'blocked'.
	if _, err := tx.ExecContext(ctx,
		`UPDATE plan SET next_check_at = NOW(), updated_at = NOW() WHERE id = $1`,
		task.PlanID); err != nil {
		return BlockOutcomeBlocked, fmt.Errorf("poke plan: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return BlockOutcomeBlocked, fmt.Errorf("commit block: %w", err)
	}
	return BlockOutcomeBlocked, nil
}

