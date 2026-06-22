package http

// Agent Planning M1 — the agent-facing plan/create endpoint. Lives
// on the internal-mTLS engine because the only legitimate caller is
// the executor's agent/create_plan action (M1 commit 7) which carries
// an mTLS cert. Future plan-management surface (plan/get_status,
// plan/cancel, plan/revise) lives at /api/v1/agent/:id/plan/... and
// is M3 work — not in this commit.
//
// Validation layers, applied in order:
//
//   1. JSON schema / required-field decoding (gin.ShouldBindJSON)
//   2. Structural integrity of the task graph
//      - Task name uniqueness within the plan
//      - All depends_on references resolve to another task in the plan
//      - All ${name.output} refs in inputs reference an upstream task
//        (transitive via depends_on)
//      - No cycles in the depends_on graph (topological sort succeeds)
//   3. Per-task flow_revision existence
//   4. Persistence: CreatePlanWithTasks (transactional)
//   5. Side-effects: next_check_at = now, audit event
//
// Each validation failure returns a structured 400 with a
// task_name field so the executor's action can surface it back to
// the AI for self-correction (rather than just "plan invalid").

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

// createPlanRequest is the public wire shape of POST
// /api/v1/internal/agent/:agentID/plan. Field names match the M1
// plan-doc body example verbatim.
type createPlanRequest struct {
	Title                string           `json:"title"`
	Goal                 string           `json:"goal"`
	Tasks                []createPlanTask `json:"tasks"`
	OwnerUserID          *string          `json:"owner_user_id,omitempty"`
	OrganisationID       *string          `json:"organisation_id,omitempty"`
	CreatedByExecutionID *string          `json:"created_by_execution_id,omitempty"`
}

// createPlanTask is one entry in the request's tasks array.
//
// M1.5 made flow_id + flow_revision_id OPTIONAL — when present
// together, the task dispatches via the pinned flow (kind='flow').
// When both are absent, the task defaults to orchestrator dispatch
// — the tick fires the agent's orchestrator flow via the Plan Task
// Trigger, and the AI handles the work. Specifying only one of
// flow_id / flow_revision_id is rejected with a structured 400
// (partial_flow_ref) so the agent can fix the request.
//
// DependsOn carries names (not UUIDs) at the wire — the handler
// translates name → UUID before persistence. Inputs is the raw JSON
// object so types (numbers, nested objects, etc.) round-trip
// verbatim.
type createPlanTask struct {
	Name           string          `json:"name"`
	Description    *string         `json:"description,omitempty"`
	FlowID         *string         `json:"flow_id,omitempty"`
	FlowRevisionID *string         `json:"flow_revision_id,omitempty"`
	DependsOn      []string        `json:"depends_on,omitempty"`
	Inputs         json.RawMessage `json:"inputs,omitempty"`
	NotBefore      *time.Time      `json:"not_before,omitempty"`
	MaxAttempts    int             `json:"max_attempts,omitempty"`
	TimeoutSeconds *int            `json:"timeout_seconds,omitempty"`
}

// deriveTaskKind returns the persistence-shape task_kind for a wire
// task, or a structured error describing the discriminated-union
// shape violation. The three legal shapes are:
//
//   - neither flow_id nor flow_revision_id set → "orchestrator"
//   - both flow_id and flow_revision_id set    → "flow"
//   - exactly one set                          → partial_flow_ref error
func deriveTaskKind(t createPlanTask) (string, map[string]interface{}) {
	hasFlow := t.FlowID != nil && *t.FlowID != ""
	hasRev := t.FlowRevisionID != nil && *t.FlowRevisionID != ""
	switch {
	case !hasFlow && !hasRev:
		return api.PlanTaskKindOrchestrator, nil
	case hasFlow && hasRev:
		return api.PlanTaskKindFlow, nil
	default:
		return "", map[string]interface{}{
			"reason":    "partial_flow_ref",
			"task_name": t.Name,
			"detail":    "specify both flow_id and flow_revision_id, or neither",
		}
	}
}

// createPlan handles POST /api/v1/internal/agent/:agentID/plan.
//
// On success returns 201 with the new plan_id and task count. On
// validation failure returns 400 with a structured detail object;
// on unexpected DB failure returns 500.
func (s *Service) createPlan(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent id required"})
		return
	}

	var req createPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "schema",
			"detail": err.Error(),
		})
		return
	}

	if strings.TrimSpace(req.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title required"})
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "goal required"})
		return
	}
	if len(req.Tasks) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one task required"})
		return
	}

	// Structural validation (task graph). Returns a JSON-marshalable
	// detail map so the caller can route to the right task.
	if detail := validatePlanTasks(req.Tasks); detail != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "validation",
			"detail": detail,
		})
		return
	}

	// Per-task kind derivation + flow_revision verification.
	// Orchestrator-kind tasks (no flow_id / flow_revision_id) skip
	// the verification step — there's no pin to validate. The
	// derived kind is cached in taskKinds so the persistence-build
	// loop below can stamp it without re-running deriveTaskKind.
	taskKinds := make([]string, len(req.Tasks))
	for i, t := range req.Tasks {
		kind, kindErr := deriveTaskKind(t)
		if kindErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "validation",
				"detail": kindErr,
			})
			return
		}
		taskKinds[i] = kind
		if kind != api.PlanTaskKindFlow {
			continue
		}
		// kind == 'flow' — verify the pinned revision exists.
		ok, err := s.persistence.VerifyFlowRevision(*t.FlowID, *t.FlowRevisionID)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id":         agentID,
				"task_name":        t.Name,
				"flow_id":          *t.FlowID,
				"flow_revision_id": *t.FlowRevisionID,
				"error":            err,
			}).Error("plan/create: flow revision verify failed")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":            "unknown_flow_revision",
				"task_name":        t.Name,
				"flow_id":          *t.FlowID,
				"flow_revision_id": *t.FlowRevisionID,
			})
			return
		}
	}

	// Pre-assign UUIDs so each task's DependsOn (names from the wire)
	// can be translated to UUID[] before INSERT. The persistence layer
	// accepts a populated task.ID via COALESCE.
	taskIDs := make(map[string]string, len(req.Tasks))
	for _, t := range req.Tasks {
		taskIDs[t.Name] = uuid.NewString()
	}

	// Build the persistence-shape tasks.
	tasks := make([]*api.PlanTask, len(req.Tasks))
	for i, t := range req.Tasks {
		dependsUUIDs := make(pq.StringArray, 0, len(t.DependsOn))
		for _, depName := range t.DependsOn {
			dependsUUIDs = append(dependsUUIDs, taskIDs[depName])
		}
		maxAttempts := t.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		inputs := t.Inputs
		if len(inputs) == 0 {
			inputs = json.RawMessage("{}")
		}
		// Stamp the persistence shape with the derived kind. For
		// orchestrator-kind tasks FlowID and FlowRevisionID remain
		// nil; for flow-kind they carry the validated pin. The schema
		// CHECK constraint enforces this exactly-one shape at the
		// row level — Go nullability + the persistence handoff
		// keep the application side honest.
		tasks[i] = &api.PlanTask{
			ID:             taskIDs[t.Name],
			Name:           t.Name,
			Description:    t.Description,
			TaskKind:       taskKinds[i],
			FlowID:         t.FlowID,
			FlowRevisionID: t.FlowRevisionID,
			Status:         "pending",
			DependsOn:      dependsUUIDs,
			NotBefore:      t.NotBefore,
			InputsJSON:     inputs,
			MaxAttempts:    maxAttempts,
			TimeoutSeconds: t.TimeoutSeconds,
		}
	}

	plan := &api.Plan{
		AgentID:              agentID,
		OwnerUserID:          req.OwnerUserID,
		OrganisationID:       req.OrganisationID,
		CreatedByExecutionID: req.CreatedByExecutionID,
		Title:                req.Title,
		Goal:                 req.Goal,
		Status:               "active",
	}
	if err := s.persistence.CreatePlanWithTasks(plan, tasks); err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Error("plan/create: persistence failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Wake the orchestrator immediately. SetPlanNextCheck failure is
	// logged but doesn't abort — the next poller scan will pick the
	// plan up either way (the plan_ready_tick_idx partial index covers
	// next_check_at NULL via NULLS FIRST).
	if err := s.persistence.SetPlanNextCheck(plan.ID, time.Now()); err != nil {
		log.WithField("plan_id", plan.ID).Warn("plan/create: SetPlanNextCheck failed; relying on next poller scan")
	}

	// Audit event. Same fire-and-forget posture as the next-check
	// poke — losing this row is annoying for the timeline view but
	// not load-bearing for plan execution.
	eventData, _ := json.Marshal(map[string]interface{}{
		"task_count": len(tasks),
	})
	if err := s.persistence.CreatePlanEvent(&api.PlanEvent{
		PlanID:    plan.ID,
		EventType: "plan_created",
		Data:      eventData,
	}); err != nil {
		log.WithField("plan_id", plan.ID).Warn("plan/create: audit event insert failed")
	}

	c.JSON(http.StatusCreated, gin.H{
		"plan_id":    plan.ID,
		"task_count": len(tasks),
		"status":     plan.Status,
	})
}

// validatePlanTasks runs the structural checks listed in
// plans/agent_planning_m1.md commit 4. Returns a JSON-marshalable
// detail object on the first failure, or nil when the graph is valid.
//
// We deliberately fail fast on the first issue rather than collecting
// all violations: agents iterate one issue at a time and a long
// detail list inflates the tool-result tokens for diminishing return.
func validatePlanTasks(tasks []createPlanTask) map[string]interface{} {
	// 1. Name uniqueness.
	seen := make(map[string]int, len(tasks))
	for i, t := range tasks {
		if t.Name == "" {
			return map[string]interface{}{
				"reason":     "missing_name",
				"task_index": i,
			}
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

	// 2. Every depends_on entry must resolve to another task name.
	for _, t := range tasks {
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

	// 3. ${name.X} refs in inputs must reference an UPSTREAM task —
	// "upstream" = reachable via depends_on, including transitively.
	// We compute the transitive closure once per task; M1 task counts
	// are small enough that O(N^2) is fine.
	for _, t := range tasks {
		ancestors := collectAncestors(t.Name, tasks)
		if len(t.Inputs) == 0 {
			continue
		}
		refs := extractTaskRefNames(t.Inputs)
		for _, refName := range refs {
			// Refs to non-existent tasks are silently allowed at
			// substitution time (the executor's later namespaces get
			// a shot), so the validator only flags refs to KNOWN
			// tasks that are NOT upstream — a forward/sibling
			// reference that would never resolve at tick time.
			if _, isPlanTask := seen[refName]; !isPlanTask {
				continue
			}
			if _, isUpstream := ancestors[refName]; !isUpstream {
				return map[string]interface{}{
					"reason":     "ref_not_upstream",
					"task_name":  t.Name,
					"referenced": refName,
				}
			}
		}
	}

	// 4. Cycle detection via Kahn's topological sort. If the sort
	// fails to consume all nodes, a cycle exists.
	if cycleTaskName, ok := detectCycle(tasks); ok {
		return map[string]interface{}{
			"reason":    "cycle",
			"task_name": cycleTaskName,
		}
	}

	return nil
}

// collectAncestors returns the set of tasks reachable from `name` via
// depends_on (transitive). The starting task is excluded — we want
// "what comes BEFORE me," not "including me."
func collectAncestors(name string, tasks []createPlanTask) map[string]bool {
	byName := make(map[string]createPlanTask, len(tasks))
	for _, t := range tasks {
		byName[t.Name] = t
	}
	out := make(map[string]bool)
	var visit func(string)
	visit = func(n string) {
		t, ok := byName[n]
		if !ok {
			return
		}
		for _, dep := range t.DependsOn {
			if out[dep] {
				continue
			}
			out[dep] = true
			visit(dep)
		}
	}
	visit(name)
	return out
}

// extractTaskRefNames scans the JSON for ${name.X} patterns and
// returns the unique task names referenced. Used by the
// "ref_not_upstream" check. Re-uses the persistence package's regex
// for consistency.
func extractTaskRefNames(raw json.RawMessage) []string {
	matches := persistence.TaskRefNamesIn(string(raw))
	return matches
}

// detectCycle returns the name of a task involved in a cycle, or
// ("", true) when the graph is acyclic. Kahn's algorithm: repeatedly
// remove nodes with no incoming edges; if any nodes remain, they
// form a cycle.
func detectCycle(tasks []createPlanTask) (string, bool) {
	indegree := make(map[string]int, len(tasks))
	successors := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		if _, ok := indegree[t.Name]; !ok {
			indegree[t.Name] = 0
		}
		for _, dep := range t.DependsOn {
			indegree[t.Name]++
			successors[dep] = append(successors[dep], t.Name)
		}
	}
	queue := make([]string, 0, len(tasks))
	for name, d := range indegree {
		if d == 0 {
			queue = append(queue, name)
		}
	}
	processed := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		processed++
		for _, succ := range successors[n] {
			indegree[succ]--
			if indegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}
	if processed == len(tasks) {
		return "", false
	}
	// Find any task with remaining indegree > 0 — that's where the
	// cycle lives.
	for name, d := range indegree {
		if d > 0 {
			return name, true
		}
	}
	return fmt.Sprintf("unknown (%d unprocessed)", len(tasks)-processed), true
}
