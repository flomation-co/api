package persistence

// Plan persistence — the CRUD layer behind the agent-planning HTTP
// endpoints (M1 commits 4-6) and the Launch-side tick orchestrator
// (M1 commit 8). See plans/agent_planning.md and
// plans/agent_planning_m1.md for the design.
//
// Design notes:
//
//   * CreatePlanWithTasks is the only multi-row writer — both the
//     plan/create endpoint and the executor's plan/create action
//     guarantee tasks land atomically with their parent plan, or
//     nothing lands. Single-row helpers (CreatePlan, CreatePlanTask)
//     exist for the rare cases (cancellation, manual repair) where a
//     plan needs to evolve without its full task set.
//
//   * VerifyFlowRevision is a small lookup the plan/create handler
//     uses to validate every task's (flow_id, flow_revision_id) pin
//     before insertion — we want a clean 400 here rather than a FK
//     violation surfacing later, especially because flow_revision_id
//     is deliberately NOT FK-enforced at the schema level (see
//     migration 99's file header for why).
//
//   * GetPlanForUpdate uses SELECT ... FOR UPDATE — the tick endpoint
//     holds a row lock on the plan while it walks tasks and dispatches
//     executions, preventing two concurrent ticks from double-firing
//     the same task. Cheap given plan tick volume is low.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"flomation.app/automate/api"
)

// ErrPlanNotFound is the sentinel for missing-plan lookups. Wrapped
// by the HTTP layer into a 404 response.
var ErrPlanNotFound = errors.New("plan not found")

// ErrFlowRevisionNotFound is returned by VerifyFlowRevision when the
// (flow_id, revision_id) pair doesn't exist. plan/create surfaces
// this as a 400 with the specific task name + revision id so the
// agent can correct its plan and retry.
var ErrFlowRevisionNotFound = errors.New("flow revision not found")

// CreatePlan inserts a single plan row. Used by repair / cancellation
// flows where the tasks already exist; the normal path is
// CreatePlanWithTasks.
func (s *Service) CreatePlan(plan *api.Plan) error {
	return s.conn.Get(plan, planInsertSQL,
		plan.AgentID,
		plan.OwnerUserID,
		plan.OrganisationID,
		plan.CreatedByExecutionID,
		plan.Title,
		plan.Goal,
		plan.Status,
		plan.NextCheckAt,
	)
}

// CreatePlanTask inserts a single plan_task row. The caller is
// expected to have populated task.ID (a UUID generated client-side)
// so that sibling tasks' depends_on arrays — which carry UUIDs, not
// names — can reference this task before the transaction commits.
// Passing an empty task.ID falls back to the column DEFAULT
// (gen_random_uuid()) for repair/manual flows where no sibling
// references the new task.
func (s *Service) CreatePlanTask(task *api.PlanTask) error {
	kind := task.TaskKind
	if kind == "" {
		kind = api.PlanTaskKindOrchestrator
	}
	return s.conn.Get(task, planTaskInsertSQL,
		nullableID(task.ID),
		task.PlanID,
		task.Name,
		task.Description,
		kind,
		task.FlowID,
		task.FlowRevisionID,
		task.Status,
		task.DependsOn,
		task.NotBefore,
		task.InputsJSON,
		task.MaxAttempts,
		task.TimeoutSeconds,
	)
}

// nullableID returns a *string the SQL layer treats as NULL when the
// id is empty (so the schema DEFAULT fires) and as the caller's value
// otherwise. Saves repeating the same COALESCE dance at every call
// site.
func nullableID(id string) interface{} {
	if id == "" {
		return nil
	}
	return id
}

// CreatePlanWithTasks inserts a plan and all of its tasks atomically.
// This is the canonical writer used by plan/create. If any task
// insert fails the entire transaction rolls back so the database
// never carries a half-built plan.
//
// The plan and each task in `tasks` are mutated in place to carry the
// server-generated id, created_at, updated_at fields (the inserts use
// RETURNING * via the planInsertSQL/planTaskInsertSQL constants).
func (s *Service) CreatePlanWithTasks(plan *api.Plan, tasks []*api.PlanTask) error {
	tx, err := s.conn.Beginx()
	if err != nil {
		return fmt.Errorf("begin plan tx: %w", err)
	}
	defer func() {
		// Rollback is safe on a committed tx (no-op).
		_ = tx.Rollback()
	}()

	if err := tx.Get(plan, planInsertSQL,
		plan.AgentID,
		plan.OwnerUserID,
		plan.OrganisationID,
		plan.CreatedByExecutionID,
		plan.Title,
		plan.Goal,
		plan.Status,
		plan.NextCheckAt,
	); err != nil {
		return fmt.Errorf("insert plan: %w", err)
	}

	for i, t := range tasks {
		// Bind the just-generated plan ID — the agent supplied tasks
		// without knowing the plan ID yet, so we stamp it here.
		t.PlanID = plan.ID
		kind := t.TaskKind
		if kind == "" {
			kind = api.PlanTaskKindOrchestrator
		}
		if err := tx.Get(t, planTaskInsertSQL,
			nullableID(t.ID),
			t.PlanID,
			t.Name,
			t.Description,
			kind,
			t.FlowID,
			t.FlowRevisionID,
			t.Status,
			t.DependsOn,
			t.NotBefore,
			t.InputsJSON,
			t.MaxAttempts,
			t.TimeoutSeconds,
		); err != nil {
			return fmt.Errorf("insert plan_task[%d] %q: %w", i, t.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plan: %w", err)
	}
	return nil
}

// CreatePlanEvent appends to the plan-event audit timeline.
func (s *Service) CreatePlanEvent(event *api.PlanEvent) error {
	return s.conn.Get(event, planEventInsertSQL,
		event.PlanID,
		event.PlanTaskID,
		event.EventType,
		event.Data,
	)
}

// CountPlansCreatedByAgentSince returns the number of plans the
// agent has created at or after the given timestamp. Backs the M3.5
// per-agent rate cap on plan/create — the handler rejects creates
// when this returns ≥ 1 within a short window (default 10s), which
// catches the AI-second-guesses-itself pattern where the model
// calls plan/create twice in close succession off a single user
// message.
//
// Counts ALL statuses (including cancelled): the cap is about
// creation frequency, not active-plan headcount. A user who just
// cancelled a plan should still get rate-limited if they ask the
// agent to immediately create another within the window.
func (s *Service) CountPlansCreatedByAgentSince(agentID string, since time.Time) (int, error) {
	var n int
	err := s.conn.Get(&n,
		`SELECT COUNT(*) FROM plan WHERE agent_id = $1 AND created_at >= $2`,
		agentID, since)
	return n, err
}

// GetPlanByID returns a single plan or ErrPlanNotFound. Does NOT load
// the task list — call GetPlanTasksByPlanID separately when needed.
func (s *Service) GetPlanByID(id string) (*api.Plan, error) {
	var plan api.Plan
	err := s.conn.Get(&plan, `SELECT * FROM plan WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlanNotFound
		}
		return nil, err
	}
	return &plan, nil
}

// GetPlanForUpdate is the tick endpoint's entry point. Takes a row
// lock on the plan for the duration of the surrounding transaction
// so concurrent ticks can't double-fire tasks. Use only inside a tx.
func (s *Service) GetPlanForUpdate(ctx context.Context, tx *sqlx.Tx, id string) (*api.Plan, error) {
	var plan api.Plan
	err := tx.GetContext(ctx, &plan,
		`SELECT * FROM plan WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlanNotFound
		}
		return nil, err
	}
	return &plan, nil
}

// GetPlanTasksByPlanID returns all tasks for a plan in stable order
// (creation order — Postgres returns insert order via PK in practice
// for UUID PKs given the gen_random_uuid() generation; we sort
// explicitly on created_at for clarity).
func (s *Service) GetPlanTasksByPlanID(planID string) ([]*api.PlanTask, error) {
	var tasks []*api.PlanTask
	err := s.conn.Select(&tasks,
		`SELECT * FROM plan_task WHERE plan_id = $1 ORDER BY created_at ASC`, planID)
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListPlansByAgentID returns the page of plans for one agent plus the
// total matching count (so the HTTP layer can set the x-total-items
// header for pagination). The optional statusFilter narrows by the
// plan.status enum; empty string returns all statuses.
//
// Ordered newest-first to match the editor's typical "what happened
// most recently?" framing — opposite to GetPlanTasksByPlanID, which
// sorts oldest-first because the task ordering carries semantic
// meaning (dependencies and dispatch order).
func (s *Service) ListPlansByAgentID(agentID, statusFilter string, limit, offset int) ([]*api.Plan, int, error) {
	var (
		plans []*api.Plan
		total int
	)

	whereClause := "agent_id = $1"
	args := []interface{}{agentID}
	if statusFilter != "" {
		whereClause += " AND status = $2"
		args = append(args, statusFilter)
	}

	countQuery := "SELECT COUNT(*) FROM plan WHERE " + whereClause
	if err := s.conn.Get(&total, countQuery, args...); err != nil {
		return nil, 0, err
	}

	// Limit + offset land at the end of the argument list. Built
	// dynamically because the optional status filter changes the
	// placeholder index.
	listQuery := "SELECT * FROM plan WHERE " + whereClause +
		" ORDER BY created_at DESC LIMIT $" + intToPlaceholder(len(args)+1) +
		" OFFSET $" + intToPlaceholder(len(args)+2)
	args = append(args, limit, offset)

	if err := s.conn.Select(&plans, listQuery, args...); err != nil {
		return nil, 0, err
	}
	return plans, total, nil
}

// ListPlanEventsByPlanID returns up to `limit` events for the plan,
// newest-first. The optional `before` cursor lets the editor page
// backwards through history without scanning the full event table —
// callers pass the timestamp of the oldest event currently shown to
// fetch the next page.
//
// The event_id PK is BIGSERIAL so a strict (created_at, id) tiebreak
// is safe — two events with the same created_at are still ordered
// deterministically by ID.
func (s *Service) ListPlanEventsByPlanID(planID string, limit int, before *time.Time) ([]*api.PlanEvent, error) {
	var events []*api.PlanEvent

	if before == nil {
		err := s.conn.Select(&events, `
			SELECT * FROM plan_event
			WHERE plan_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2`, planID, limit)
		return events, err
	}

	err := s.conn.Select(&events, `
		SELECT * FROM plan_event
		WHERE plan_id = $1 AND created_at < $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, planID, *before, limit)
	return events, err
}

// intToPlaceholder formats an integer as the Postgres placeholder
// suffix (the digits after the $). Kept inline rather than importing
// strconv at the top of the file to keep this file's import set tight.
func intToPlaceholder(n int) string {
	return fmt.Sprintf("%d", n)
}

// VerifyFlowRevision checks that (flow_id, revision_id) exists. The
// plan/create handler calls this for every task before inserting, so
// a wrong revision id surfaces as a clean 400 rather than failing
// the dispatch much later.
//
// Returns (true, nil) on a match, (false, nil) on a clean miss, and
// (false, err) on actual DB failure. The miss case is a normal
// validation outcome, not an error.
func (s *Service) VerifyFlowRevision(flowID, revisionID string) (bool, error) {
	var exists bool
	err := s.conn.Get(&exists, `
		SELECT EXISTS (
			SELECT 1 FROM flo_revision
			WHERE id = $1 AND flo_id = $2
		)`, revisionID, flowID)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// SetPlanNextCheck updates next_check_at so the tick poller knows
// when to look at this plan again. Used by the tick endpoint when a
// plan has tasks gated by not_before — we set next_check_at to the
// earliest not_before so the poller doesn't waste a tick before
// anything could be ready.
func (s *Service) SetPlanNextCheck(planID string, at time.Time) error {
	_, err := s.conn.Exec(
		`UPDATE plan SET next_check_at = $1, updated_at = NOW() WHERE id = $2`,
		at, planID)
	return err
}

// planInsertSQL is shared between CreatePlan (direct) and
// CreatePlanWithTasks (transactional). RETURNING * round-trips the
// server-generated columns (id, created_at, updated_at) into the
// caller's struct so subsequent reads aren't needed.
const planInsertSQL = `
	INSERT INTO plan (
		agent_id, owner_user_id, organisation_id, created_by_execution_id,
		title, goal, status, next_check_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	RETURNING *`

// planTaskInsertSQL — same pattern as planInsertSQL. The
// outputs_json, execution_id, attempt_count, last_error, started_at
// and completed_at columns deliberately use their schema defaults
// (NULL or 0) rather than being set explicitly — they get populated
// by the tick/completion path, not at create time.
//
// planTaskInsertSQL accepts an explicit id ($1) which can be NULL —
// in which case the column DEFAULT (gen_random_uuid()) fires. The
// plan/create handler supplies an id so depends_on UUIDs translated
// from task names line up at insert time; CreatePlanTask (repair
// path) leaves it nil.
//
// task_kind ($5) is set by the caller and must match the CHECK
// constraint on the column (see migration 100). flow_id and
// flow_revision_id (now nullable since M1.5) are passed via $6 and
// $7 — Go nil → SQL NULL.
const planTaskInsertSQL = `
	INSERT INTO plan_task (
		id, plan_id, name, description, task_kind,
		flow_id, flow_revision_id, status,
		depends_on, not_before, inputs_json, max_attempts, timeout_seconds
	) VALUES (
		COALESCE($1::uuid, gen_random_uuid()),
		$2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
	)
	RETURNING *`

const planEventInsertSQL = `
	INSERT INTO plan_event (plan_id, plan_task_id, event_type, data)
	VALUES ($1, $2, $3, $4)
	RETURNING *`

// SetPlanEventListener wires the post-commit publish hook used by
// the transactional plan helpers (TickPlan, HandlePlanTaskCompletion,
// BlockPlanTask). Called once at HTTP service startup to bind
// PlanEventHub.Publish. Safe to leave unset — persistence functions
// check the field for nil before invoking.
func (s *Service) SetPlanEventListener(fn PlanEventListener) {
	s.planEventListener = fn
}

// publishPlanEvents fans out a slice of inserted PlanEvent rows to
// the listener. Called only AFTER tx.Commit() returns nil so a
// rollback never leaks phantom events. No-op when the listener is
// nil or the slice is empty.
func (s *Service) publishPlanEvents(events []*api.PlanEvent) {
	if s.planEventListener == nil {
		return
	}
	for _, ev := range events {
		if ev != nil {
			s.planEventListener(ev)
		}
	}
}
