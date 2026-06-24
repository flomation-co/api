package persistence

// Plan revise — user- or AI-initiated modification of a plan's
// task graph. M5 closes the lifecycle by allowing adds, removes,
// and updates on plans in any non-terminal status. Authoring
// happens via plan/create; revision via plan/revise; the rest of
// the lifecycle (start, cancel, block) already shipped in M3/M4.
//
// Design highlights:
//
//   * Operations apply atomically in one transaction. Either all
//     changes land or none — partial revisions corrupt the graph.
//
//   * Validation runs against the PROJECTED post-revise task set,
//     not the pre-revise state. This lets a single revise add a
//     task AND another task that depends_on it.
//
//   * Status-protective rules: in_progress and completed tasks
//     are frozen. Removes / full-updates require pending /
//     failed / cancelled status. Description-only updates allowed
//     on any non-terminal task.
//
//   * Blocked → active auto-transition: if revise eliminates the
//     last failed task (by removing or replacing it), the plan
//     transitions back to active and the tick poker fires.
//
// The validator here is intentionally LOCAL (not reusing the HTTP-
// layer createPlanTask validator) — persistence shouldn't depend
// on http types. The rules are simple enough to duplicate.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"flomation.app/automate/api"
)

// RevisionOutcome reports the result of a revise attempt.
type RevisionOutcome string

const (
	RevisionOutcomeRevised  RevisionOutcome = "revised"
	RevisionOutcomeNotFound RevisionOutcome = "not_found"
	RevisionOutcomeTerminal RevisionOutcome = "terminal" // completed/cancelled
	RevisionOutcomeInvalid  RevisionOutcome = "invalid"  // validation rejection
)

// RevisionTask is one new task in a revise add list. Same shape as
// createPlanTask (HTTP layer) but kept independent so persistence
// doesn't depend on http types.
type RevisionTask struct {
	Name           string          `json:"name"`
	Description    *string         `json:"description,omitempty"`
	FlowID         *string         `json:"flow_id,omitempty"`
	FlowRevisionID *string         `json:"flow_revision_id,omitempty"`
	DependsOn      []string        `json:"depends_on,omitempty"` // names
	Inputs         json.RawMessage `json:"inputs,omitempty"`
	NotBefore      *time.Time      `json:"not_before,omitempty"`
	MaxAttempts    int             `json:"max_attempts,omitempty"`
	TimeoutSeconds *int            `json:"timeout_seconds,omitempty"`
}

// RevisionUpdate identifies an existing task by name and provides
// optional field overrides. Nil pointers / empty json mean "leave
// this field unchanged". DependsOn uses *[]string so the caller
// can distinguish "leave alone" (nil) from "clear all deps" ([]).
type RevisionUpdate struct {
	Name           string          `json:"name"`
	Description    *string         `json:"description,omitempty"`
	FlowID         *string         `json:"flow_id,omitempty"`
	FlowRevisionID *string         `json:"flow_revision_id,omitempty"`
	DependsOn      *[]string       `json:"depends_on,omitempty"`
	Inputs         json.RawMessage `json:"inputs,omitempty"`
	MaxAttempts    *int            `json:"max_attempts,omitempty"`
	TimeoutSeconds *int            `json:"timeout_seconds,omitempty"`
}

// RevisionOps is the full set of changes to apply in one revise.
// Any of the three slices may be empty/nil.
type RevisionOps struct {
	AddTasks    []RevisionTask   `json:"add_tasks,omitempty"`
	RemoveTasks []string         `json:"remove_tasks,omitempty"`
	UpdateTasks []RevisionUpdate `json:"update_tasks,omitempty"`
}

// RevisionResult is what RevisePlan returns. ErrorDetail is
// populated when Outcome == Invalid so the HTTP layer can surface
// it verbatim to the caller (the AI uses it to self-correct).
type RevisionResult struct {
	Outcome     RevisionOutcome
	ErrorDetail map[string]interface{}
	NewStatus   string
	AddedIDs    []string
	RemovedIDs  []string
	UpdatedIDs  []string
}

// RevisePlan applies a batch of task-graph changes atomically. The
// validator inside the transaction is authoritative — even if the
// HTTP handler pre-checked, this is the gate that actually decides
// success.
func (s *Service) RevisePlan(ctx context.Context, planID string, ops RevisionOps) (RevisionResult, error) {
	tx, err := s.conn.BeginTxx(ctx, nil)
	if err != nil {
		return RevisionResult{Outcome: RevisionOutcomeNotFound},
			fmt.Errorf("begin revise tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := reviseGetPlanForUpdate(ctx, tx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RevisionResult{Outcome: RevisionOutcomeNotFound}, nil
		}
		return RevisionResult{Outcome: RevisionOutcomeNotFound},
			fmt.Errorf("lock plan: %w", err)
	}

	// Status gate: terminal plans can't be revised.
	if plan.Status == "completed" || plan.Status == "cancelled" {
		return RevisionResult{Outcome: RevisionOutcomeTerminal, NewStatus: plan.Status}, nil
	}

	// Load current tasks (also locked transitively via FK row
	// locks acquired by subsequent UPDATEs/DELETEs).
	var existing []*api.PlanTask
	if err := tx.SelectContext(ctx, &existing,
		`SELECT * FROM plan_task WHERE plan_id = $1 ORDER BY created_at ASC`,
		planID); err != nil {
		return RevisionResult{Outcome: RevisionOutcomeNotFound},
			fmt.Errorf("load tasks: %w", err)
	}

	// Build name → task map for fast lookup during validation.
	byName := make(map[string]*api.PlanTask, len(existing))
	for _, t := range existing {
		byName[t.Name] = t
	}

	// === Pre-projection per-op rules ===
	if detail := preProjectionChecks(ops, existing, byName); detail != nil {
		return RevisionResult{Outcome: RevisionOutcomeInvalid, ErrorDetail: detail}, nil
	}

	// === Project the post-revise task set in memory ===
	// We pre-assign UUIDs for added tasks so other adds in the
	// same batch can reference them by name AND we can persist
	// depends_on UUID arrays consistently.
	projected, addedUUIDByName := projectPostRevise(existing, ops)

	// === Validate the projection ===
	if detail := validateProjection(projected); detail != nil {
		return RevisionResult{Outcome: RevisionOutcomeInvalid, ErrorDetail: detail}, nil
	}

	now := time.Now()

	// === Apply DB writes ===
	// Order: removes → updates → adds. Removes first so a name
	// freed by a remove can be reused by an add in the same batch.
	// Updates before adds so update's depends_on resolution
	// (against the projection) doesn't see ghost task rows.

	var removedIDs []string
	for _, name := range ops.RemoveTasks {
		task := byName[name]
		removedIDs = append(removedIDs, task.ID)
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plan_task WHERE id = $1`, task.ID); err != nil {
			return RevisionResult{Outcome: RevisionOutcomeNotFound},
				fmt.Errorf("remove task %q: %w", name, err)
		}
	}

	var updatedIDs []string
	for _, u := range ops.UpdateTasks {
		task := byName[u.Name]
		if err := applyUpdate(ctx, tx, task, u, addedUUIDByName, byName); err != nil {
			return RevisionResult{Outcome: RevisionOutcomeNotFound},
				fmt.Errorf("update task %q: %w", u.Name, err)
		}
		updatedIDs = append(updatedIDs, task.ID)
	}

	var addedIDs []string
	for _, a := range ops.AddTasks {
		newID := addedUUIDByName[a.Name]
		addedIDs = append(addedIDs, newID)
		if err := applyAdd(ctx, tx, planID, newID, a, addedUUIDByName, byName); err != nil {
			return RevisionResult{Outcome: RevisionOutcomeNotFound},
				fmt.Errorf("add task %q: %w", a.Name, err)
		}
	}

	// === Re-derive plan status ===
	// Only meaningful transition: blocked → active when there are
	// no failed tasks left. Draft and active stay where they are.
	newStatus := plan.Status
	pokeNextCheck := false
	if plan.Status == "blocked" {
		var failedCount int
		if err := tx.GetContext(ctx, &failedCount,
			`SELECT COUNT(*) FROM plan_task WHERE plan_id = $1 AND status = 'failed'`,
			planID); err != nil {
			return RevisionResult{Outcome: RevisionOutcomeNotFound},
				fmt.Errorf("count failed tasks: %w", err)
		}
		if failedCount == 0 {
			newStatus = "active"
			pokeNextCheck = true
		}
	} else if plan.Status == "active" {
		// New pending tasks in an active plan should be picked up
		// on the next tick.
		if len(ops.AddTasks) > 0 {
			pokeNextCheck = true
		}
	}

	if newStatus != plan.Status || pokeNextCheck {
		var nextCheck interface{} = nil
		if pokeNextCheck {
			nextCheck = now
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE plan
			SET status = $2, next_check_at = $3, updated_at = NOW()
			WHERE id = $1`, planID, newStatus, nextCheck); err != nil {
			return RevisionResult{Outcome: RevisionOutcomeNotFound},
				fmt.Errorf("update plan status: %w", err)
		}
	}

	// === Audit event ===
	eventData, _ := json.Marshal(map[string]interface{}{
		"added_count":   len(addedIDs),
		"removed_count": len(removedIDs),
		"updated_count": len(updatedIDs),
		"new_status":    newStatus,
		"prev_status":   plan.Status,
	})
	ev, evErr := tickInsertPlanEvent(ctx, tx, planID, nil, "plan_revised", eventData)
	if evErr != nil {
		return RevisionResult{Outcome: RevisionOutcomeNotFound},
			fmt.Errorf("audit plan_revised: %w", evErr)
	}

	if err := tx.Commit(); err != nil {
		return RevisionResult{Outcome: RevisionOutcomeNotFound},
			fmt.Errorf("commit revise: %w", err)
	}

	// Post-commit publish.
	s.publishPlanEvents([]*api.PlanEvent{ev})

	return RevisionResult{
		Outcome:    RevisionOutcomeRevised,
		NewStatus:  newStatus,
		AddedIDs:   addedIDs,
		RemovedIDs: removedIDs,
		UpdatedIDs: updatedIDs,
	}, nil
}

// reviseGetPlanForUpdate locks the plan row. Plain FOR UPDATE (not
// SKIP LOCKED) so a concurrent tick serialises rather than yields.
func reviseGetPlanForUpdate(ctx context.Context, tx *sqlx.Tx, planID string) (*api.Plan, error) {
	var plan api.Plan
	err := tx.GetContext(ctx, &plan,
		`SELECT * FROM plan WHERE id = $1 FOR UPDATE`, planID)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

// === Validation helpers ============================================

// preProjectionChecks runs the rules that depend on each task's
// CURRENT status — e.g. "you can't remove an in_progress task".
// These checks are stricter than the post-projection validator
// because they look at task lifecycle state, not just graph shape.
func preProjectionChecks(ops RevisionOps, existing []*api.PlanTask, byName map[string]*api.PlanTask) map[string]interface{} {
	// Removes must target existing pending/failed/cancelled tasks.
	for _, name := range ops.RemoveTasks {
		task, ok := byName[name]
		if !ok {
			return map[string]interface{}{
				"reason":    "remove_unknown_task",
				"task_name": name,
			}
		}
		switch task.Status {
		case "pending", "failed", "cancelled":
			// allowed
		default:
			return map[string]interface{}{
				"reason":      "cannot_remove_task_in_status",
				"task_name":   name,
				"task_status": task.Status,
			}
		}
	}

	// Updates must target existing tasks. For now, full updates
	// (anything beyond description) require pending status.
	for _, u := range ops.UpdateTasks {
		task, ok := byName[u.Name]
		if !ok {
			return map[string]interface{}{
				"reason":    "update_unknown_task",
				"task_name": u.Name,
			}
		}
		if updateTouchesNonDescriptionFields(u) && task.Status != "pending" {
			return map[string]interface{}{
				"reason":      "cannot_update_non_pending_task",
				"task_name":   u.Name,
				"task_status": task.Status,
			}
		}
	}

	// Adds can't collide with existing names (duplicates within
	// the adds slice itself are caught by validateProjection).
	for _, a := range ops.AddTasks {
		if _, exists := byName[a.Name]; exists {
			// Check if also being removed in this batch — that's a
			// legitimate "swap" pattern.
			beingRemoved := false
			for _, r := range ops.RemoveTasks {
				if r == a.Name {
					beingRemoved = true
					break
				}
			}
			if !beingRemoved {
				return map[string]interface{}{
					"reason":    "add_collides_with_existing",
					"task_name": a.Name,
				}
			}
		}
	}

	return nil
}

// updateTouchesNonDescriptionFields returns true when the update
// modifies anything beyond description. Description-only updates
// are allowed on any non-terminal task status.
func updateTouchesNonDescriptionFields(u RevisionUpdate) bool {
	return u.FlowID != nil ||
		u.FlowRevisionID != nil ||
		u.DependsOn != nil ||
		len(u.Inputs) > 0 ||
		u.MaxAttempts != nil ||
		u.TimeoutSeconds != nil
}

// projectPostRevise applies the ops to the existing task set in
// memory and returns the projection plus a name→UUID map for the
// newly-added tasks (used downstream when persisting depends_on
// arrays which carry UUIDs, not names).
func projectPostRevise(existing []*api.PlanTask, ops RevisionOps) (projected []*api.PlanTask, addedUUIDByName map[string]string) {
	addedUUIDByName = make(map[string]string, len(ops.AddTasks))
	for _, a := range ops.AddTasks {
		addedUUIDByName[a.Name] = uuid.NewString()
	}

	// Remove set for fast lookup.
	removeSet := make(map[string]bool, len(ops.RemoveTasks))
	for _, r := range ops.RemoveTasks {
		removeSet[r] = true
	}

	// Update map for fast lookup.
	updateByName := make(map[string]RevisionUpdate, len(ops.UpdateTasks))
	for _, u := range ops.UpdateTasks {
		updateByName[u.Name] = u
	}

	// Pre-existing-not-removed, with updates applied.
	for _, t := range existing {
		if removeSet[t.Name] {
			continue
		}
		proj := *t // shallow copy — depends_on slice is shared but we treat it read-only
		if u, ok := updateByName[t.Name]; ok {
			applyUpdateToProjection(&proj, u, addedUUIDByName)
		}
		projected = append(projected, &proj)
	}

	// Plus the new adds.
	for _, a := range ops.AddTasks {
		t := &api.PlanTask{
			ID:    addedUUIDByName[a.Name],
			Name:  a.Name,
			Status: "pending",
		}
		applyAddToProjection(t, a, addedUUIDByName, projected)
		projected = append(projected, t)
	}

	return projected, addedUUIDByName
}

// applyUpdateToProjection mutates a projected task in place to
// reflect an update op's overrides. depends_on names are translated
// to UUIDs (looked up against added-in-batch + pre-existing).
func applyUpdateToProjection(t *api.PlanTask, u RevisionUpdate, addedUUIDByName map[string]string) {
	if u.Description != nil {
		t.Description = u.Description
	}
	if u.FlowID != nil {
		t.FlowID = u.FlowID
	}
	if u.FlowRevisionID != nil {
		t.FlowRevisionID = u.FlowRevisionID
	}
	// task_kind follows the (flow_id, flow_revision_id) pair after
	// the update lands.
	if t.FlowID != nil && t.FlowRevisionID != nil {
		t.TaskKind = api.PlanTaskKindFlow
	} else if t.FlowID == nil && t.FlowRevisionID == nil {
		t.TaskKind = api.PlanTaskKindOrchestrator
	}
	if u.DependsOn != nil {
		// Names → UUIDs; unresolved names are checked downstream by
		// validateProjection (we record raw names here and the
		// projection-time check sees them).
		// For simplicity, we record names temporarily; the actual
		// SQL UPDATE in applyUpdate does the translation.
		raw := make(pq.StringArray, 0, len(*u.DependsOn))
		for _, name := range *u.DependsOn {
			raw = append(raw, name) // name for now; applyUpdate translates
		}
		t.DependsOn = raw
	}
	if len(u.Inputs) > 0 {
		t.InputsJSON = u.Inputs
	}
	if u.MaxAttempts != nil {
		t.MaxAttempts = *u.MaxAttempts
	}
	if u.TimeoutSeconds != nil {
		t.TimeoutSeconds = u.TimeoutSeconds
	}
}

// applyAddToProjection populates a fresh PlanTask with the add op's
// fields. depends_on names are kept as names in the projection (the
// validateProjection step works in name-space; applyAdd does the
// final translation).
func applyAddToProjection(t *api.PlanTask, a RevisionTask, addedUUIDByName map[string]string, _ []*api.PlanTask) {
	t.Description = a.Description
	t.FlowID = a.FlowID
	t.FlowRevisionID = a.FlowRevisionID
	if a.FlowID != nil && a.FlowRevisionID != nil {
		t.TaskKind = api.PlanTaskKindFlow
	} else {
		t.TaskKind = api.PlanTaskKindOrchestrator
	}
	t.NotBefore = a.NotBefore
	maxAttempts := a.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	t.MaxAttempts = maxAttempts
	t.TimeoutSeconds = a.TimeoutSeconds
	t.InputsJSON = a.Inputs
	if len(t.InputsJSON) == 0 {
		t.InputsJSON = json.RawMessage("{}")
	}
	// depends_on as names — translated in applyAdd.
	raw := make(pq.StringArray, 0, len(a.DependsOn))
	for _, n := range a.DependsOn {
		raw = append(raw, n)
	}
	t.DependsOn = raw
}

// validateProjection runs the graph-shape checks over the projected
// post-revise task set. Operates in NAME-space — depends_on entries
// are still names at this point.
func validateProjection(projected []*api.PlanTask) map[string]interface{} {
	// Name uniqueness already protected by the pre-checks +
	// projection logic; assert again defensively.
	seen := make(map[string]int, len(projected))
	for i, t := range projected {
		if t.Name == "" {
			return map[string]interface{}{"reason": "missing_name", "task_index": i}
		}
		if prev, ok := seen[t.Name]; ok {
			return map[string]interface{}{
				"reason":          "duplicate_task_name",
				"task_name":       t.Name,
				"first_at_index":  prev,
				"second_at_index": i,
			}
		}
		seen[t.Name] = i
	}

	// depends_on resolves + no self-reference.
	for _, t := range projected {
		for _, dep := range t.DependsOn {
			if dep == t.Name {
				return map[string]interface{}{
					"reason":    "self_dependency",
					"task_name": t.Name,
				}
			}
			if _, ok := seen[dep]; !ok {
				return map[string]interface{}{
					"reason":     "unknown_dependency",
					"task_name":  t.Name,
					"depends_on": dep,
				}
			}
		}
	}

	// Partial flow_ref check (mirrors M1.5 createPlan rule).
	for _, t := range projected {
		hasFlow := t.FlowID != nil && *t.FlowID != ""
		hasRev := t.FlowRevisionID != nil && *t.FlowRevisionID != ""
		if hasFlow != hasRev {
			return map[string]interface{}{
				"reason":    "partial_flow_ref",
				"task_name": t.Name,
				"detail":    "specify both flow_id and flow_revision_id, or neither",
			}
		}
	}

	// Cycle detection via Kahn's topological sort.
	if cyclicTask, ok := detectCycleProjection(projected); ok {
		return map[string]interface{}{
			"reason":    "cycle",
			"task_name": cyclicTask,
		}
	}

	// Remove-with-dependents check: any depends_on that points at a
	// removed task would fail the "unknown_dependency" check above,
	// but we want a clearer error message. Already covered by
	// unknown_dependency — keep this comment as a marker.

	return nil
}

// detectCycleProjection runs Kahn's topological sort over the
// name-space depends_on graph. Returns the name of a task involved
// in a cycle on first detection; (and true) signals the cycle.
func detectCycleProjection(tasks []*api.PlanTask) (string, bool) {
	indegree := make(map[string]int, len(tasks))
	byName := make(map[string]*api.PlanTask, len(tasks))
	for _, t := range tasks {
		indegree[t.Name] = len(t.DependsOn)
		byName[t.Name] = t
	}

	// Kahn's: peel tasks with indegree 0 until none remain.
	queue := make([]string, 0)
	for name, d := range indegree {
		if d == 0 {
			queue = append(queue, name)
		}
	}

	processed := 0
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		processed++
		// Decrement indegree of every task that lists `head` as a
		// dependency.
		for _, t := range tasks {
			for _, dep := range t.DependsOn {
				if dep == head {
					indegree[t.Name]--
					if indegree[t.Name] == 0 {
						queue = append(queue, t.Name)
					}
				}
			}
		}
	}

	if processed == len(tasks) {
		return "", false
	}
	for name, d := range indegree {
		if d > 0 {
			return name, true
		}
	}
	return fmt.Sprintf("unknown (%d unprocessed)", len(tasks)-processed), true
}

// === DB-side mutation helpers =====================================

// applyUpdate persists a single update. depends_on names are
// translated to UUIDs against the byName + addedUUIDByName maps.
func applyUpdate(ctx context.Context, tx *sqlx.Tx, task *api.PlanTask, u RevisionUpdate, addedUUIDByName map[string]string, byName map[string]*api.PlanTask) error {
	sets := make([]string, 0, 8)
	args := make([]interface{}, 0, 8)
	args = append(args, task.ID)
	idx := 2

	if u.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", idx))
		args = append(args, u.Description)
		idx++
	}
	if u.FlowID != nil {
		sets = append(sets, fmt.Sprintf("flow_id = $%d", idx))
		args = append(args, nullableID(*u.FlowID))
		idx++
	}
	if u.FlowRevisionID != nil {
		sets = append(sets, fmt.Sprintf("flow_revision_id = $%d", idx))
		args = append(args, nullableID(*u.FlowRevisionID))
		idx++
	}
	// task_kind follows the projection's derived value.
	if u.FlowID != nil || u.FlowRevisionID != nil {
		kind := api.PlanTaskKindOrchestrator
		hasFlow := false
		hasRev := false
		// Look at the resulting task state — use projection's update.
		// Simplest re-derivation: read the to-be-final values.
		if u.FlowID != nil {
			hasFlow = *u.FlowID != ""
		} else {
			hasFlow = task.FlowID != nil && *task.FlowID != ""
		}
		if u.FlowRevisionID != nil {
			hasRev = *u.FlowRevisionID != ""
		} else {
			hasRev = task.FlowRevisionID != nil && *task.FlowRevisionID != ""
		}
		if hasFlow && hasRev {
			kind = api.PlanTaskKindFlow
		}
		sets = append(sets, fmt.Sprintf("task_kind = $%d", idx))
		args = append(args, kind)
		idx++
	}
	if u.DependsOn != nil {
		uuids := make(pq.StringArray, 0, len(*u.DependsOn))
		for _, name := range *u.DependsOn {
			if id, ok := resolveTaskID(name, addedUUIDByName, byName); ok {
				uuids = append(uuids, id)
			}
		}
		sets = append(sets, fmt.Sprintf("depends_on = $%d", idx))
		args = append(args, uuids)
		idx++
	}
	if len(u.Inputs) > 0 {
		sets = append(sets, fmt.Sprintf("inputs_json = $%d", idx))
		args = append(args, u.Inputs)
		idx++
	}
	if u.MaxAttempts != nil {
		sets = append(sets, fmt.Sprintf("max_attempts = $%d", idx))
		args = append(args, *u.MaxAttempts)
		idx++
	}
	if u.TimeoutSeconds != nil {
		sets = append(sets, fmt.Sprintf("timeout_seconds = $%d", idx))
		args = append(args, *u.TimeoutSeconds)
		// No idx++ — this is the last placeholder. Tracked anyway
		// in case a future field is appended below.
		_ = idx
	}
	if len(sets) == 0 {
		return nil // no-op update
	}
	sets = append(sets, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE plan_task SET %s WHERE id = $1",
		joinComma(sets))
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// applyAdd inserts one new task. depends_on names → UUIDs via the
// projection's name→id map (which already covers both pre-existing
// tasks and added-in-batch siblings).
func applyAdd(ctx context.Context, tx *sqlx.Tx, planID, newID string, a RevisionTask, addedUUIDByName map[string]string, byName map[string]*api.PlanTask) error {
	uuids := make(pq.StringArray, 0, len(a.DependsOn))
	for _, name := range a.DependsOn {
		if id, ok := resolveTaskID(name, addedUUIDByName, byName); ok {
			uuids = append(uuids, id)
		}
	}
	maxAttempts := a.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	inputs := a.Inputs
	if len(inputs) == 0 {
		inputs = json.RawMessage("{}")
	}
	kind := api.PlanTaskKindOrchestrator
	if a.FlowID != nil && a.FlowRevisionID != nil {
		kind = api.PlanTaskKindFlow
	}
	_, err := tx.ExecContext(ctx, planTaskInsertSQL,
		nullableID(newID),
		planID,
		a.Name,
		a.Description,
		kind,
		a.FlowID,
		a.FlowRevisionID,
		"pending",
		uuids,
		a.NotBefore,
		inputs,
		maxAttempts,
		a.TimeoutSeconds,
	)
	return err
}

// resolveTaskID returns the UUID for a task name, looking up either
// in added-in-batch or pre-existing-not-removed maps.
func resolveTaskID(name string, addedUUIDByName map[string]string, byName map[string]*api.PlanTask) (string, bool) {
	if id, ok := addedUUIDByName[name]; ok {
		return id, true
	}
	if t, ok := byName[name]; ok {
		return t.ID, true
	}
	return "", false
}

// joinComma is a tiny helper to avoid pulling strings just for this.
func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}
