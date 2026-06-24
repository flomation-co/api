package persistence

// Plan start — user- or AI-initiated transition from draft to
// active. M4 introduces draft-by-default for plan/create: the agent
// authors a plan as a draft, summarises it to the user, and waits
// for approval before calling plan/start. The user can also start
// drafts directly via the editor's Start button.
//
// Mechanically symmetric with plan_cancel.go (M3): lock the plan,
// transition the status, poke next_check_at so the tick poller
// picks it up immediately, audit a plan_started event, post-commit
// publish.
//
// The transition is gated on the current status being 'draft' —
// starting an already-active plan is a no-op (idempotent), starting
// a terminal plan (completed, cancelled) is a hard error.

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

// StartOutcome reports what happened. Distinguishes the three
// states the caller cares about: we just started (transitioned),
// we're already active (idempotent no-op), or the plan is already
// terminal (caller error — surface as 409 to the client).
type StartOutcome string

const (
	StartOutcomeStarted         StartOutcome = "started"
	StartOutcomeIdempotent      StartOutcome = "idempotent"
	StartOutcomeNotFound        StartOutcome = "not_found"
	StartOutcomeAlreadyTerminal StartOutcome = "already_terminal"
)

// StartPlan transitions a draft plan to active, pokes the tick
// poller, audits a plan_started event, and publishes the event
// post-commit. Returns:
//
//   - StartOutcomeStarted: was draft, now active. Tick will fire.
//   - StartOutcomeIdempotent: was already active. No-op.
//   - StartOutcomeNotFound: plan doesn't exist.
//   - StartOutcomeAlreadyTerminal: was completed or cancelled. The
//     caller should NOT silently treat this as success — the plan
//     can't be resurrected.
func (s *Service) StartPlan(ctx context.Context, planID string) (StartOutcome, error) {
	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return StartOutcomeNotFound, fmt.Errorf("begin start tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := startGetPlanForUpdate(ctx, tx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StartOutcomeNotFound, nil
		}
		return StartOutcomeNotFound, fmt.Errorf("lock plan: %w", err)
	}

	switch plan.Status {
	case "active":
		return StartOutcomeIdempotent, nil
	case "completed", "cancelled":
		return StartOutcomeAlreadyTerminal, nil
	case "blocked":
		// Blocked is non-terminal in M1.5 — a stuck plan COULD
		// theoretically be unblocked by starting. But M4's contract
		// is specifically draft → active. Treat blocked as terminal
		// from the start endpoint's perspective; revival of blocked
		// plans is M5+ work via plan/revise.
		return StartOutcomeAlreadyTerminal, nil
	case "draft":
		// proceed
	default:
		return StartOutcomeAlreadyTerminal, fmt.Errorf("unexpected plan status %q", plan.Status)
	}

	now := time.Now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE plan
		SET status = 'active',
		    next_check_at = $2,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'draft'`, plan.ID, now); err != nil {
		return StartOutcomeNotFound, fmt.Errorf("transition plan: %w", err)
	}

	eventData, _ := json.Marshal(map[string]interface{}{
		"transitioned_from": "draft",
	})
	ev, evErr := tickInsertPlanEvent(ctx, tx, plan.ID, nil, "plan_started", eventData)
	if evErr != nil {
		return StartOutcomeStarted, fmt.Errorf("audit plan_started: %w", evErr)
	}

	if err := tx.Commit(); err != nil {
		return StartOutcomeStarted, fmt.Errorf("commit start: %w", err)
	}

	// Tx committed — publish to SSE subscribers.
	s.publishPlanEvents([]*api.PlanEvent{ev})

	return StartOutcomeStarted, nil
}

// startGetPlanForUpdate locks the plan row for the duration of the
// start transaction. Uses plain FOR UPDATE (not SKIP LOCKED) — a
// concurrent tick reading the same row should serialise rather than
// yield, so we don't race against an in-flight tick.
func startGetPlanForUpdate(ctx context.Context, tx *sqlx.Tx, planID string) (*api.Plan, error) {
	var plan api.Plan
	err := tx.GetContext(ctx, &plan,
		`SELECT * FROM plan WHERE id = $1 FOR UPDATE`, planID)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}
