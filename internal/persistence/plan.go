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
	return s.conn.Get(task, planTaskInsertSQL,
		nullableID(task.ID),
		task.PlanID,
		task.Name,
		task.Description,
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
		if err := tx.Get(t, planTaskInsertSQL,
			nullableID(t.ID),
			t.PlanID,
			t.Name,
			t.Description,
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
// planTaskInsertSQL accepts an explicit id ($1) which can be NULL —
// in which case the column DEFAULT (gen_random_uuid()) fires. The
// plan/create handler supplies an id so depends_on UUIDs translated
// from task names line up at insert time; CreatePlanTask (repair
// path) leaves it nil.
const planTaskInsertSQL = `
	INSERT INTO plan_task (
		id, plan_id, name, description, flow_id, flow_revision_id, status,
		depends_on, not_before, inputs_json, max_attempts, timeout_seconds
	) VALUES (
		COALESCE($1::uuid, gen_random_uuid()),
		$2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
	)
	RETURNING *`

const planEventInsertSQL = `
	INSERT INTO plan_event (plan_id, plan_task_id, event_type, data)
	VALUES ($1, $2, $3, $4)
	RETURNING *`
