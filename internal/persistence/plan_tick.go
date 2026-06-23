package persistence

// Plan tick — the orchestration step that walks an active plan,
// finds tasks whose dependencies are satisfied, dispatches their
// flow executions, updates plan + task status, and persists the
// next check time. See plans/agent_planning_m1.md commit 5.
//
// Everything in this file runs inside a single transaction held
// open via SELECT ... FOR UPDATE on the plan row, so:
//
//   * Two concurrent ticks on the same plan can't double-fire a
//     task. The second tick gets sql.ErrNoRows from the lock query
//     and bows out cleanly — the HTTP layer maps that to a 409.
//
//   * Status counts, dispatches, and next_check_at all settle
//     atomically. Crash mid-tick rolls everything back; the next
//     poll will see the plan in the same state and try again.
//
// Plan status derivation rules (see derivePlanStatus):
//
//   - all tasks completed             → "completed"
//   - any pending or in_progress task → "active" (no change)
//   - no progress possible AND at least one failed/cancelled task
//     AND no pending/in_progress     → "blocked"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"flomation.app/automate/api"
)

// MaxConcurrentPlanTasks caps how many tasks for a single plan can
// be in_progress at once. Keeps a runaway plan from saturating the
// runner pool. The plan doc reserves room for raising this per-plan
// later (M3+); M1 ships a global constant.
const MaxConcurrentPlanTasks = 5

// GlobalAttemptCap is the hard limit on per-task retries regardless
// of plan_task.max_attempts. Prevents an agent setting max_attempts:
// 9999 and burning quota on a hopeless task.
const GlobalAttemptCap = 10

// FiredTask is one entry in the TickPlanResult's fired list — the
// HTTP response surfaces this so the caller (typically the Launch
// poller, but also a manual tick via curl) can see what was newly
// dispatched.
type FiredTask struct {
	TaskID      string `json:"task_id"`
	TaskName    string `json:"task_name"`
	ExecutionID string `json:"execution_id"`
}

// TickPlanResult is what TickPlan returns to the HTTP layer.
type TickPlanResult struct {
	PlanID      string         `json:"plan_id"`
	PlanStatus  string         `json:"plan_status"`
	Fired       []FiredTask    `json:"fired"`
	Counts      map[string]int `json:"counts"`
	NextCheckAt *time.Time     `json:"next_check_at,omitempty"`
}

// ErrPlanLocked is returned when SELECT ... FOR UPDATE SKIP LOCKED
// finds the row but another transaction holds the lock. The HTTP
// layer translates this to a 409 so the caller knows to retry
// later rather than failing hard.
var ErrPlanLocked = errors.New("plan is being ticked by another instance")

// ErrPlanTerminal indicates the plan is already in a terminal
// status (completed/cancelled). The HTTP layer maps this to a 204
// — there's nothing to tick.
var ErrPlanTerminal = errors.New("plan is in a terminal status")

// ListReadyPlanIDs returns plan IDs whose next_check_at is now or
// past (NULL counts as ready via the partial index's NULLS FIRST
// ordering), capped at `limit`. Used by the API-side plan tick
// poller (M1 commit 8) to batch-walk active plans without holding
// row locks across the whole scan — the per-plan lock is taken
// inside TickPlan.
func (s *Service) ListReadyPlanIDs(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	var ids []string
	err := s.conn.Select(&ids, `
		SELECT id FROM plan
		WHERE status = 'active'
		  AND (next_check_at IS NULL OR next_check_at <= NOW())
		ORDER BY next_check_at NULLS FIRST
		LIMIT $1`, limit)
	return ids, err
}

// TickPlan runs one tick cycle on the named plan and returns the
// post-tick state. Always opens its own transaction; callers must
// not pass one in.
func (s *Service) TickPlan(ctx context.Context, planID string) (*TickPlanResult, error) {
	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tick tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := tickGetPlanForUpdate(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if isPlanTerminal(plan.Status) {
		return nil, ErrPlanTerminal
	}

	inProgress, err := tickCountTasksByStatus(ctx, tx, planID, "in_progress")
	if err != nil {
		return nil, fmt.Errorf("count in_progress: %w", err)
	}
	budget := MaxConcurrentPlanTasks - inProgress
	if budget < 0 {
		budget = 0
	}

	// pendingEvents collects rows inserted during this tick. Published
	// via planEventListener AFTER tx.Commit() so a rollback never
	// leaks phantom events to SSE subscribers. See plan.go's
	// SetPlanEventListener for the wiring.
	var pendingEvents []*api.PlanEvent

	var fired []FiredTask
	if budget > 0 {
		ready, err := tickGetReadyTasks(ctx, tx, planID, budget)
		if err != nil {
			return nil, fmt.Errorf("get ready tasks: %w", err)
		}

		// Pre-fetch completed task outputs once for substitution. Each
		// fired task reads from this map; no need to re-query per task.
		outputs, err := tickGetCompletedTaskOutputs(ctx, tx, planID)
		if err != nil {
			return nil, fmt.Errorf("get completed outputs: %w", err)
		}

		for _, task := range ready {
			resolved, err := SubstituteTaskRefs(task.InputsJSON, outputs)
			if err != nil {
				return nil, fmt.Errorf("substitute task %q: %w", task.Name, err)
			}

			// agentLookup is a lazy closure — orchestrator-kind dispatch
			// needs the agent record (for OrchestratorFlowID), flow-kind
			// dispatch doesn't. Lazy so we don't pay the lookup cost on
			// flow-only plans. The closure caches the result so a plan
			// with many orchestrator tasks only loads the agent once.
			var cachedAgent *api.Agent
			agentLookup := func(agentID string) (*api.Agent, error) {
				if cachedAgent != nil {
					return cachedAgent, nil
				}
				ag, lookupErr := s.GetAgentByID(agentID)
				if lookupErr != nil {
					return nil, lookupErr
				}
				cachedAgent = ag
				return ag, nil
			}

			execID, err := tickCreateTaskExecution(ctx, tx, plan, task, resolved, outputs, agentLookup)
			if err != nil {
				return nil, fmt.Errorf("create execution for task %q: %w", task.Name, err)
			}

			if err := tickMarkTaskInProgress(ctx, tx, task.ID, execID); err != nil {
				return nil, fmt.Errorf("mark task %q in_progress: %w", task.Name, err)
			}

			eventData, _ := json.Marshal(map[string]interface{}{
				"execution_id": execID,
			})
			ev, evErr := tickInsertPlanEvent(ctx, tx, plan.ID, &task.ID, "task_started", eventData)
			if evErr != nil {
				return nil, fmt.Errorf("audit task_started: %w", evErr)
			}
			pendingEvents = append(pendingEvents, ev)

			fired = append(fired, FiredTask{
				TaskID:      task.ID,
				TaskName:    task.Name,
				ExecutionID: execID,
			})
		}
	}

	// Re-read status counts AFTER firing so the derivation reflects
	// the just-dispatched in_progress tasks.
	counts, err := tickGetTaskStatusCounts(ctx, tx, planID)
	if err != nil {
		return nil, fmt.Errorf("status counts: %w", err)
	}

	newStatus := derivePlanStatus(plan.Status, counts)

	// next_check_at: if there are still pending tasks gated by
	// not_before, pick the earliest such time. Otherwise NULL —
	// the next external poke (writeback hook or manual call) wakes us.
	nextCheck, err := tickComputeNextCheck(ctx, tx, planID)
	if err != nil {
		return nil, fmt.Errorf("compute next check: %w", err)
	}

	if err := tickUpdatePlanStatusAndNextCheck(ctx, tx, planID, newStatus, nextCheck); err != nil {
		return nil, fmt.Errorf("update plan status: %w", err)
	}

	if newStatus != plan.Status {
		ev, evErr := tickInsertPlanEvent(ctx, tx, planID, nil, "plan_"+newStatus, nil)
		if evErr != nil {
			return nil, fmt.Errorf("audit plan status change: %w", evErr)
		}
		pendingEvents = append(pendingEvents, ev)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tick: %w", err)
	}

	// Tx committed — safe to publish. publishPlanEvents is a no-op
	// when no listener is wired (e.g. background poller startup
	// before HTTP service init).
	s.publishPlanEvents(pendingEvents)

	return &TickPlanResult{
		PlanID:      planID,
		PlanStatus:  newStatus,
		Fired:       fired,
		Counts:      counts,
		NextCheckAt: nextCheck,
	}, nil
}

// === tx-scoped helpers (lower-case; not part of the public service
// surface so the HTTP layer can't reach them mid-tick without going
// through TickPlan) ===

// tickGetPlanForUpdate fetches the plan with SELECT ... FOR UPDATE
// SKIP LOCKED. Two callers compete: the winner gets the row; the
// loser gets sql.ErrNoRows and we surface that as ErrPlanLocked.
func tickGetPlanForUpdate(ctx context.Context, tx *sqlx.Tx, planID string) (*api.Plan, error) {
	var plan api.Plan
	err := tx.GetContext(ctx, &plan,
		`SELECT * FROM plan WHERE id = $1 FOR UPDATE SKIP LOCKED`, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Could be missing OR concurrently locked. Distinguish via
			// a probe read — cheap because the row is either there or
			// it isn't.
			var probe int
			perr := tx.GetContext(ctx, &probe,
				`SELECT 1 FROM plan WHERE id = $1`, planID)
			if errors.Is(perr, sql.ErrNoRows) {
				return nil, ErrPlanNotFound
			}
			return nil, ErrPlanLocked
		}
		return nil, err
	}
	return &plan, nil
}

func tickCountTasksByStatus(ctx context.Context, tx *sqlx.Tx, planID, status string) (int, error) {
	var n int
	err := tx.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM plan_task WHERE plan_id = $1 AND status = $2`,
		planID, status)
	return n, err
}

// tickGetReadyTasks returns up to `limit` tasks whose dependencies
// are all completed and whose not_before (if set) has passed. The
// NOT EXISTS subquery is the load-bearing bit: a task is ready
// only when EVERY dep id in its depends_on array has a completed
// row in plan_task.
func tickGetReadyTasks(ctx context.Context, tx *sqlx.Tx, planID string, limit int) ([]*api.PlanTask, error) {
	var tasks []*api.PlanTask
	err := tx.SelectContext(ctx, &tasks, `
		SELECT * FROM plan_task t
		WHERE t.plan_id = $1
		  AND t.status = 'pending'
		  AND (t.not_before IS NULL OR t.not_before <= NOW())
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest(t.depends_on) AS dep_id
		    WHERE NOT EXISTS (
		      SELECT 1 FROM plan_task d
		      WHERE d.id = dep_id AND d.status = 'completed'
		    )
		  )
		ORDER BY t.created_at
		LIMIT $2`, planID, limit)
	return tasks, err
}

// tickGetCompletedTaskOutputs returns the (task_name → outputs_json
// map) mapping that SubstituteTaskRefs consumes. We key on task
// NAME, not UUID, because the substitution syntax uses names.
func tickGetCompletedTaskOutputs(ctx context.Context, tx *sqlx.Tx, planID string) (map[string]map[string]interface{}, error) {
	type row struct {
		Name    string          `db:"name"`
		Outputs json.RawMessage `db:"outputs_json"`
	}
	var rows []row
	err := tx.SelectContext(ctx, &rows, `
		SELECT name, outputs_json
		FROM plan_task
		WHERE plan_id = $1
		  AND status = 'completed'
		  AND outputs_json IS NOT NULL`, planID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]interface{}, len(rows))
	for _, r := range rows {
		var m map[string]interface{}
		if len(r.Outputs) == 0 {
			continue
		}
		if err := json.Unmarshal(r.Outputs, &m); err != nil {
			// Skip tasks whose outputs aren't a top-level object.
			// Substitution can't pick into them anyway.
			continue
		}
		out[r.Name] = m
	}
	return out, nil
}

// tickCreateTaskExecution inserts a plan-task execution row directly
// (no trigger_invocation — plan tasks aren't trigger-fired). Parent
// linkage flows through the existing resolveParent helper so the
// execution slots into the hierarchical tree the same way any other
// child execution does.
func tickCreateTaskExecution(ctx context.Context, tx *sqlx.Tx, plan *api.Plan, task *api.PlanTask, resolvedInputs json.RawMessage, upstream map[string]map[string]interface{}, agentLookup func(string) (*api.Agent, error)) (string, error) {
	switch task.TaskKind {
	case api.PlanTaskKindFlow, "":
		// Empty fallback preserves the pre-M1.5 row shape where
		// task_kind didn't exist and every row was implicitly a flow.
		return tickCreateFlowTaskExecution(ctx, tx, plan, task, resolvedInputs)
	case api.PlanTaskKindOrchestrator:
		return tickCreateOrchestratorTaskExecution(ctx, tx, plan, task, resolvedInputs, upstream, agentLookup)
	default:
		return "", fmt.Errorf("plan task %s: unknown task_kind %q", task.ID, task.TaskKind)
	}
}

// tickCreateFlowTaskExecution preserves M1's exact behaviour for
// kind='flow' tasks — INSERT an execution against the pinned flow
// with parent linkage. Refactored out of the old single-branch
// function so the orchestrator-kind sibling can sit next to it.
func tickCreateFlowTaskExecution(ctx context.Context, tx *sqlx.Tx, plan *api.Plan, task *api.PlanTask, resolvedInputs json.RawMessage) (string, error) {
	if task.FlowID == nil {
		return "", fmt.Errorf("plan task %s: kind=flow but flow_id is nil — schema CHECK should prevent this", task.ID)
	}
	executionID := uuid.NewString()
	execution := api.Execution{
		ID:               executionID,
		FloID:            *task.FlowID,
		OwnerID:          plan.AgentID, // agent owns plan executions
		OrganisationID:   plan.OrganisationID,
		Data:             resolvedInputs,
		ExecutionStatus:  "created",
		CompletionStatus: "pending",
		RootExecutionID:  executionID,
	}

	// Parent linkage: if the plan was created BY an execution (the
	// agent's orchestrator flow that called plan/create), every task
	// hangs off that parent. Without it, plan tasks would appear as
	// roots in the executions tree — visually disconnected from the
	// agent turn that produced them.
	if plan.CreatedByExecutionID != nil && *plan.CreatedByExecutionID != "" {
		rootID, depth, capped, err := resolveParentInTx(ctx, tx, *plan.CreatedByExecutionID)
		if err != nil {
			return "", err
		}
		parentID := *plan.CreatedByExecutionID
		execution.ParentExecutionID = &parentID
		rel := "plan_task"
		execution.ParentRelationship = &rel
		metadata, _ := json.Marshal(map[string]interface{}{
			"plan_id":        plan.ID,
			"plan_title":     plan.Title,
			"plan_task_id":   task.ID,
			"plan_task_name": task.Name,
		})
		if capped {
			merged, _ := mergeDepthCappedFlag(metadata)
			metadata = merged
		}
		raw := json.RawMessage(metadata)
		execution.ParentMetadata = &raw
		execution.RootExecutionID = rootID
		execution.Depth = depth
	} else {
		// Top-of-tree plan execution (no orchestrator parent). The
		// parent_metadata still carries plan_id so the writeback
		// hook (commit 6) can find the matching plan_task row.
		metadata, _ := json.Marshal(map[string]interface{}{
			"plan_id":        plan.ID,
			"plan_task_id":   task.ID,
			"plan_task_name": task.Name,
		})
		raw := json.RawMessage(metadata)
		execution.ParentMetadata = &raw
	}

	if task.Description != nil {
		execution.Name = *task.Description
	} else {
		execution.Name = fmt.Sprintf("plan task: %s", task.Name)
	}

	_, err := tx.NamedExecContext(ctx, `
		INSERT INTO execution (
			id, flo_id, name, owner_id, organisation_id, data,
			execution_status, completion_status, root_execution_id, depth,
			parent_execution_id, parent_relationship, parent_metadata
		) VALUES (
			:id, :flo_id, :name, :owner_id, :organisation_id, :data,
			:execution_status, :completion_status, :root_execution_id, :depth,
			:parent_execution_id, :parent_relationship, :parent_metadata
		)`, execution)
	if err != nil {
		return "", err
	}
	return executionID, nil
}

// resolveParentInTx mirrors Service.resolveParent (execution.go) but
// runs against the supplied tx. Kept inline here to avoid exposing
// the package-level resolveParent's tx-vs-conn ambiguity through a
// new public surface.
func resolveParentInTx(ctx context.Context, tx *sqlx.Tx, parentID string) (rootID string, depth int, capped bool, err error) {
	var parent struct {
		RootID string `db:"root_execution_id"`
		Depth  int    `db:"depth"`
	}
	err = tx.GetContext(ctx, &parent,
		`SELECT root_execution_id, depth FROM execution WHERE id = $1`, parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No parent → tick creates an orphan-ish execution. The
			// caller already checked the pointer was set, so a miss
			// here is an integrity issue, not a normal path.
			return "", 0, false, fmt.Errorf("parent execution %s not found", parentID)
		}
		return "", 0, false, err
	}
	rootID = parent.RootID
	depth = parent.Depth + 1
	if depth > MaxExecutionDepth {
		depth = MaxExecutionDepth
		capped = true
	}
	return rootID, depth, capped, nil
}

func tickMarkTaskInProgress(ctx context.Context, tx *sqlx.Tx, taskID, executionID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE plan_task
		SET status = 'in_progress',
		    execution_id = $2,
		    attempt_count = attempt_count + 1,
		    started_at = COALESCE(started_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1`, taskID, executionID)
	return err
}

func tickGetTaskStatusCounts(ctx context.Context, tx *sqlx.Tx, planID string) (map[string]int, error) {
	type row struct {
		Status string `db:"status"`
		N      int    `db:"n"`
	}
	var rows []row
	err := tx.SelectContext(ctx, &rows, `
		SELECT status, COUNT(*) AS n
		FROM plan_task
		WHERE plan_id = $1
		GROUP BY status`, planID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

// tickComputeNextCheck picks the earliest not_before among pending
// tasks. NULL when no time-gated tasks remain — the next poker
// (writeback hook or manual API call) wakes us. The poller's partial
// index handles "NULL means now" via NULLS FIRST.
func tickComputeNextCheck(ctx context.Context, tx *sqlx.Tx, planID string) (*time.Time, error) {
	var t sql.NullTime
	err := tx.GetContext(ctx, &t, `
		SELECT MIN(not_before)
		FROM plan_task
		WHERE plan_id = $1
		  AND status = 'pending'
		  AND not_before IS NOT NULL
		  AND not_before > NOW()`, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}

func tickUpdatePlanStatusAndNextCheck(ctx context.Context, tx *sqlx.Tx, planID, status string, nextCheck *time.Time) error {
	completedAt := sql.NullTime{}
	if status == "completed" {
		completedAt.Time = time.Now()
		completedAt.Valid = true
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE plan
		SET status = $2,
		    next_check_at = $3,
		    completed_at = COALESCE(completed_at, $4),
		    updated_at = NOW()
		WHERE id = $1`, planID, status, nextCheck, completedAt)
	return err
}

// tickInsertPlanEvent inserts an audit row and returns it with the
// server-generated columns (id, created_at) populated. Callers
// COLLECT the returned events into a local slice and publish them
// via Service.planEventListener AFTER successful tx.Commit() — that
// ordering guarantees rollbacks don't leak phantom events into the
// SSE stream.
func tickInsertPlanEvent(ctx context.Context, tx *sqlx.Tx, planID string, taskID *string, eventType string, data json.RawMessage) (*api.PlanEvent, error) {
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	var ev api.PlanEvent
	err := tx.GetContext(ctx, &ev, `
		INSERT INTO plan_event (plan_id, plan_task_id, event_type, data)
		VALUES ($1, $2, $3, $4)
		RETURNING *`, planID, taskID, eventType, data)
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

// isPlanTerminal returns true when there's nothing more to do.
// Cancelled is intentionally terminal even though tasks could
// technically still complete — once a user cancels we stop firing.
func isPlanTerminal(status string) bool {
	return status == "completed" || status == "cancelled"
}

// derivePlanStatus returns the post-tick plan status given the
// current status and the task-status histogram. Pure function so
// it's cheaply unit-testable.
//
// Rules (in order):
//
//  1. Any pending OR in_progress task → "active" (no change).
//  2. All tasks completed → "completed".
//  3. No active tasks, at least one failed OR cancelled, no
//     completed-but-zero scenario → "blocked".
//  4. Fallback to existing status (defensive — covers an empty
//     plan or anything unanticipated).
func derivePlanStatus(current string, counts map[string]int) string {
	pending := counts["pending"]
	inProgress := counts["in_progress"]
	completed := counts["completed"]
	failed := counts["failed"]
	cancelled := counts["cancelled"]
	total := pending + inProgress + completed + failed + cancelled

	if pending > 0 || inProgress > 0 {
		return "active"
	}
	if total > 0 && completed == total {
		return "completed"
	}
	if total > 0 && (failed > 0 || cancelled > 0) {
		return "blocked"
	}
	return current
}

// === Orchestrator-kind dispatch (M1.5 commit 3) ===

// tickCreateOrchestratorTaskExecution fires the agent's own
// orchestrator flow via its Plan Task Trigger entry node. Same
// dispatch pattern Telegram / Slack channel webhooks use — INSERT a
// trigger_invocation, then an execution linked to it. The runner picks
// up the execution, reads trigger_invocation.trigger_id → trigger row
// → __node_id in trigger data, and starts the executor at the Plan
// Task Trigger node.
//
// Trigger data is shaped to match the Plan Task Trigger node's
// declared outputs (see actions/trigger/plan_task/action.go). The
// downstream AI Prompt action reads `${flow.prompt}` and
// `${flow.conversation_history}` the same way it reads them when a
// Telegram or Slack trigger fired — no flow rewiring.
func tickCreateOrchestratorTaskExecution(
	ctx context.Context, tx *sqlx.Tx,
	plan *api.Plan, task *api.PlanTask,
	resolvedInputs json.RawMessage,
	upstream map[string]map[string]interface{},
	agentLookup func(string) (*api.Agent, error),
) (string, error) {
	agent, err := agentLookup(plan.AgentID)
	if err != nil {
		return "", fmt.Errorf("load agent %s: %w", plan.AgentID, err)
	}
	if agent == nil {
		return "", fmt.Errorf("agent %s not found", plan.AgentID)
	}
	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		return "", fmt.Errorf("agent %s has no orchestrator_flow_id — plan-task dispatch requires one", agent.ID)
	}
	flowID := *agent.OrchestratorFlowID

	triggerRow, err := tickGetPlanTaskTriggerForFlow(ctx, tx, flowID)
	if err != nil {
		return "", fmt.Errorf("find plan_task trigger for flow %s: %w", flowID, err)
	}
	if triggerRow == nil {
		return "", fmt.Errorf("agent %s's orchestrator (flow %s) does not have a Plan Task Trigger node — add one to enable orchestrator-kind plan tasks", agent.ID, flowID)
	}

	// Build the trigger data shape the Plan Task Trigger reflects as
	// outputs. Field names match the node's declared Outputs.
	prompt := buildPlanTaskPrompt(plan, task, resolvedInputs, upstream)
	inputsForData := decodeInputsForTriggerData(resolvedInputs)
	upstreamForData := upstreamSafeForTriggerData(upstream)

	triggerData := map[string]interface{}{
		// task_kind discriminator the system prompt assembler reads
		// (M1.5 commit 4) to know whether to auto-augment with
		// PLAN TASK MODE instructions.
		"task_kind": "plan_task",

		// Channel-shaped fields — identical to what user-message
		// triggers populate, so the AI Prompt action wires
		// unchanged.
		"prompt":               prompt,
		"channel_type":         "plan_task",
		"channel_id":           "",
		"conversation_history": []interface{}{}, // empty — no history bleed
		"agent_id":             agent.ID,
		"agent_user_id":        "",

		// Plan-specific fields the AI's tools (set_output, plan/block
		// in commit 6) reference.
		"plan_id":               plan.ID,
		"plan_task_id":          task.ID,
		"plan_task_name":        task.Name,
		"plan_task_description": strDerefOrEmpty(task.Description),
		"plan_task_inputs":      inputsForData,
		"upstream_outputs":      upstreamForData,

		// Standard dispatch metadata the executor's runtime injects.
		"agent_id_for_context": agent.ID,
	}

	// Insert the trigger_invocation. owner_id mirrors the trigger
	// row's owner (system / agent owner — agent is the right answer
	// here since plans are owned by the agent that authored them).
	invocationData, err := json.Marshal(triggerData)
	if err != nil {
		return "", fmt.Errorf("marshal trigger data: %w", err)
	}
	var invocationID string
	if err := tx.GetContext(ctx, &invocationID, `
		INSERT INTO trigger_invocation (trigger_id, owner_id, organisation_id, data)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		triggerRow.ID, agent.OwnerID, plan.OrganisationID, invocationData,
	); err != nil {
		return "", fmt.Errorf("insert trigger_invocation: %w", err)
	}

	// Build the execution row pointing at the orchestrator flow with
	// parent linkage (so the executions hierarchy view shows the
	// plan-task execution under the orchestrator turn that created
	// the plan).
	executionID := uuid.NewString()
	execution := api.Execution{
		ID:               executionID,
		FloID:            flowID,
		OwnerID:          agent.OwnerID,
		OrganisationID:   plan.OrganisationID,
		TriggeredBy:      &invocationID,
		Data:             invocationData,
		ExecutionStatus:  "created",
		CompletionStatus: "pending",
		RootExecutionID:  executionID,
		Name:             fmt.Sprintf("plan task: %s", task.Name),
		AgentID:          &agent.ID,
	}

	// Parent linkage: same posture as flow-kind dispatch.
	if plan.CreatedByExecutionID != nil && *plan.CreatedByExecutionID != "" {
		rootID, depth, capped, perr := resolveParentInTx(ctx, tx, *plan.CreatedByExecutionID)
		if perr != nil {
			return "", perr
		}
		parentID := *plan.CreatedByExecutionID
		execution.ParentExecutionID = &parentID
		rel := "plan_task"
		execution.ParentRelationship = &rel
		metadata, _ := json.Marshal(map[string]interface{}{
			"plan_id":        plan.ID,
			"plan_title":     plan.Title,
			"plan_task_id":   task.ID,
			"plan_task_name": task.Name,
		})
		if capped {
			merged, _ := mergeDepthCappedFlag(metadata)
			metadata = merged
		}
		raw := json.RawMessage(metadata)
		execution.ParentMetadata = &raw
		execution.RootExecutionID = rootID
		execution.Depth = depth
	} else {
		metadata, _ := json.Marshal(map[string]interface{}{
			"plan_id":        plan.ID,
			"plan_task_id":   task.ID,
			"plan_task_name": task.Name,
		})
		raw := json.RawMessage(metadata)
		execution.ParentMetadata = &raw
	}

	if _, err := tx.NamedExecContext(ctx, `
		INSERT INTO execution (
			id, flo_id, name, owner_id, organisation_id, agent_id,
			triggered_by, data,
			execution_status, completion_status,
			root_execution_id, depth,
			parent_execution_id, parent_relationship, parent_metadata
		) VALUES (
			:id, :flo_id, :name, :owner_id, :organisation_id, :agent_id,
			:triggered_by, :data,
			:execution_status, :completion_status,
			:root_execution_id, :depth,
			:parent_execution_id, :parent_relationship, :parent_metadata
		)`, execution); err != nil {
		return "", fmt.Errorf("insert execution: %w", err)
	}
	return executionID, nil
}

// tickGetPlanTaskTriggerForFlow finds the Plan Task Trigger row
// (type 'plan-task' — see the typeName mangling in
// internal/http/flow.go's createFloRevision sync) registered for
// the given flow. Returns (nil, nil) when no plan_task trigger is
// configured — the caller surfaces that as a clear error so the
// agent author knows to add the Plan Task Trigger node to their
// orchestrator flow.
func tickGetPlanTaskTriggerForFlow(ctx context.Context, tx *sqlx.Tx, flowID string) (*api.Trigger, error) {
	var trigger api.Trigger
	err := tx.GetContext(ctx, &trigger, `
		SELECT t.*
		FROM trigger t
		JOIN flo_trigger ft ON ft.trigger_id = t.id
		WHERE ft.flo_id = $1 AND t.type = 'plan-task'
		ORDER BY t.created_at DESC
		LIMIT 1`, flowID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &trigger, nil
}

// buildPlanTaskPrompt formats the user-message-equivalent string the
// AI Prompt action consumes via ${flow.prompt}. Mirrors the framing
// the plan doc specifies — task name, description, inputs, upstream
// outputs, plus a terminator hint pointing the AI at set_output /
// plan/block.
func buildPlanTaskPrompt(plan *api.Plan, task *api.PlanTask, inputs json.RawMessage, upstream map[string]map[string]interface{}) string {
	var sb strings.Builder
	sb.WriteString("Progress plan task '")
	sb.WriteString(task.Name)
	sb.WriteString("' of plan '")
	sb.WriteString(plan.Title)
	sb.WriteString("'.\n\n")
	if task.Description != nil && *task.Description != "" {
		sb.WriteString("Description: ")
		sb.WriteString(*task.Description)
		sb.WriteString("\n\n")
	}
	if len(inputs) > 0 && string(inputs) != "{}" && string(inputs) != "null" {
		sb.WriteString("Inputs: ")
		sb.Write(inputs)
		sb.WriteString("\n\n")
	}
	if len(upstream) > 0 {
		b, err := json.Marshal(upstream)
		if err == nil {
			sb.WriteString("Upstream outputs: ")
			sb.Write(b)
			sb.WriteString("\n\n")
		}
	}
	sb.WriteString("Complete this task and call set_output with what downstream tasks need. If you cannot make progress, call plan/block with the reason.")
	return sb.String()
}

// decodeInputsForTriggerData turns the substituted inputs JSONB into
// a Go map for embedding in the trigger data. On any decode failure
// returns an empty map — the AI sees no inputs rather than a malformed
// payload.
func decodeInputsForTriggerData(inputs json.RawMessage) map[string]interface{} {
	if len(inputs) == 0 {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(inputs, &out); err != nil {
		return map[string]interface{}{}
	}
	if out == nil {
		return map[string]interface{}{}
	}
	return out
}

// upstreamSafeForTriggerData ensures a non-nil map lands in the
// trigger data even when no upstream tasks completed.
func upstreamSafeForTriggerData(upstream map[string]map[string]interface{}) map[string]map[string]interface{} {
	if upstream == nil {
		return map[string]map[string]interface{}{}
	}
	return upstream
}

func strDerefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
