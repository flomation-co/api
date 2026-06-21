package persistence

// Plan-task completion writeback — invoked from the API's execution
// completion handler whenever a flow execution finishes. If the
// execution belongs to a plan task (identified via its
// parent_metadata.plan_task_id field, populated by TickPlan), this
// writes back to plan_task and pokes the plan's next_check_at so the
// orchestrator runs again immediately.
//
// Why one method, not a chain? Same reason TickPlan is one method:
// the writeback is a transactional unit — find the task, transition
// it, audit, poke the plan. All-or-nothing. Splitting it into
// per-step helpers forces the HTTP handler to wrangle a tx, which
// dilutes the layering. The HTTP handler just calls this once and
// gets a structured outcome back.

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

// WritebackOutcome reports what happened. Used by the HTTP layer for
// metrics + log fields. NONE means the execution had no plan task to
// write back to (the common case — most executions aren't plan tasks).
type WritebackOutcome string

const (
	WritebackNone       WritebackOutcome = "none"
	WritebackCompleted  WritebackOutcome = "completed"
	WritebackRequeued   WritebackOutcome = "requeued"
	WritebackFailed     WritebackOutcome = "failed"
	WritebackCancelled  WritebackOutcome = "cancelled"
	WritebackIdempotent WritebackOutcome = "idempotent" // already terminal
)

// PlanTaskCompletionInput is everything HandlePlanTaskCompletion
// needs. The HTTP handler builds this from the existing
// completion-handling block in execution.go.
type PlanTaskCompletionInput struct {
	ExecutionID      string
	ParentMetadata   json.RawMessage
	CompletionStatus string          // "success" | "fail" | "cancel"
	Outputs          json.RawMessage // execution result.State
	ErrorMessage     string
}

// HandlePlanTaskCompletion runs the writeback. Returns
// WritebackNone (no error) when the execution wasn't a plan task —
// the common case — so callers can invoke this unconditionally
// from the completion handler without per-call gating.
func (s *Service) HandlePlanTaskCompletion(ctx context.Context, in PlanTaskCompletionInput) (WritebackOutcome, error) {
	planTaskID := extractPlanTaskID(in.ParentMetadata)
	if planTaskID == "" {
		return WritebackNone, nil
	}

	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return WritebackNone, fmt.Errorf("begin writeback tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := writebackGetTaskForUpdate(ctx, tx, planTaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The plan_task row was deleted (cascade from plan delete?)
			// between dispatch and completion. Nothing to do — log at
			// the call site if surprising.
			return WritebackNone, nil
		}
		return WritebackNone, fmt.Errorf("lock plan_task: %w", err)
	}

	// Idempotency guard: a writeback can fire twice for the same
	// execution (network retry, runner crash + reconnect). We only
	// transition from in_progress; anything else means another
	// writeback already won.
	if task.Status != "in_progress" {
		return WritebackIdempotent, nil
	}

	outcome, err := writebackTransitionTask(ctx, tx, task, in)
	if err != nil {
		return WritebackNone, err
	}

	// Audit event with the specific transition.
	eventType := writebackEventName(outcome)
	if eventType != "" {
		eventData, _ := json.Marshal(map[string]interface{}{
			"execution_id":  in.ExecutionID,
			"attempt_count": task.AttemptCount,
		})
		if err := tickInsertPlanEvent(ctx, tx, task.PlanID, &task.ID, eventType, eventData); err != nil {
			return outcome, fmt.Errorf("audit %s: %w", eventType, err)
		}
	}

	// Poke the plan: the next poller tick (or a manual tick) should
	// pick this up immediately so a downstream task with the just-
	// completed task as a dependency fires without waiting for the
	// next idle scan.
	if _, err := tx.ExecContext(ctx,
		`UPDATE plan SET next_check_at = NOW(), updated_at = NOW() WHERE id = $1`,
		task.PlanID); err != nil {
		return outcome, fmt.Errorf("set plan next_check: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return outcome, fmt.Errorf("commit writeback: %w", err)
	}
	return outcome, nil
}

// extractPlanTaskID pulls plan_task_id from the execution's
// parent_metadata JSON. Returns "" when the metadata is missing or
// doesn't carry the field — the signal that this execution isn't a
// plan task.
func extractPlanTaskID(parentMeta json.RawMessage) string {
	if len(parentMeta) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(parentMeta, &m); err != nil {
		return ""
	}
	s, _ := m["plan_task_id"].(string)
	return s
}

// writebackGetTaskForUpdate locks the plan_task row for the duration
// of the writeback transaction.
func writebackGetTaskForUpdate(ctx context.Context, tx *sqlx.Tx, taskID string) (*api.PlanTask, error) {
	var task api.PlanTask
	err := tx.GetContext(ctx, &task,
		`SELECT * FROM plan_task WHERE id = $1 FOR UPDATE`, taskID)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// writebackTransitionTask applies the right transition based on the
// runner's completion status. Returns the WritebackOutcome the caller
// surfaces to its audit/metrics.
func writebackTransitionTask(ctx context.Context, tx *sqlx.Tx, task *api.PlanTask, in PlanTaskCompletionInput) (WritebackOutcome, error) {
	now := time.Now()
	switch in.CompletionStatus {
	case "success":
		outputs := in.Outputs
		if len(outputs) == 0 {
			outputs = json.RawMessage("{}")
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE plan_task
			SET status = 'completed',
			    outputs_json = $2,
			    completed_at = $3,
			    updated_at = NOW(),
			    last_error = NULL
			WHERE id = $1`, task.ID, outputs, now)
		return WritebackCompleted, err

	case "fail":
		// Respect both per-task max_attempts AND the global cap.
		cap := task.MaxAttempts
		if cap > GlobalAttemptCap {
			cap = GlobalAttemptCap
		}
		if cap < 1 {
			cap = 1
		}
		// attempt_count was bumped at dispatch (TickPlan's mark-in-
		// progress UPDATE), so the comparison here is "have we
		// already exhausted the budget?"
		errMsg := truncateError(in.ErrorMessage)
		if task.AttemptCount < cap {
			// Requeue: status → pending so the next tick re-evaluates
			// dependencies and possibly re-dispatches. attempt_count
			// stays — the bump-on-dispatch convention means each new
			// in_progress transition adds one.
			_, err := tx.ExecContext(ctx, `
				UPDATE plan_task
				SET status = 'pending',
				    execution_id = NULL,
				    last_error = $2,
				    updated_at = NOW()
				WHERE id = $1`, task.ID, errMsg)
			return WritebackRequeued, err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE plan_task
			SET status = 'failed',
			    last_error = $2,
			    completed_at = $3,
			    updated_at = NOW()
			WHERE id = $1`, task.ID, errMsg, now)
		return WritebackFailed, err

	case "cancel":
		_, err := tx.ExecContext(ctx, `
			UPDATE plan_task
			SET status = 'cancelled',
			    completed_at = $2,
			    updated_at = NOW()
			WHERE id = $1`, task.ID, now)
		return WritebackCancelled, err

	default:
		// Unknown completion status — defensive no-op so a future
		// new value doesn't silently break plan transitions.
		return WritebackNone, fmt.Errorf("unknown completion status %q", in.CompletionStatus)
	}
}

// writebackEventName maps the outcome to the plan_event.event_type
// audit string. Empty string for WritebackNone — no event to write.
func writebackEventName(o WritebackOutcome) string {
	switch o {
	case WritebackCompleted:
		return "task_completed"
	case WritebackRequeued:
		return "task_retry_queued"
	case WritebackFailed:
		return "task_failed"
	case WritebackCancelled:
		return "task_cancelled"
	default:
		return ""
	}
}

// truncateError keeps the last_error column readable — runner
// failures can carry long stack traces; we want a useful preview
// without consuming the audit table.
func truncateError(msg string) string {
	const max = 2048
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "… [truncated]"
}
