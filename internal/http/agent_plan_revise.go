package http

// Agent Planning M5 — plan/revise endpoints. Pairs with M3 cancel
// + M4 start as the third user-facing mutation. Both endpoints
// reuse the M3 loadPlanForAgent helper for the agent-scope guard
// + 404 mapping.
//
// Body wire shape mirrors persistence.RevisionOps verbatim — the
// JSON tags align so a direct ShouldBindJSON works without a
// translation layer:
//
//   {
//     "add_tasks":    [ ... RevisionTask objects ... ],
//     "remove_tasks": [ "task_name", ... ],
//     "update_tasks": [ ... RevisionUpdate objects ... ]
//   }
//
// Outcome → HTTP status:
//   revised        → 200
//   not_found      → 404
//   terminal       → 409 (completed/cancelled — cannot be revised)
//   invalid        → 400 with structured detail in body

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api/internal/persistence"
)

// reviseResponse is what successful revises return. AddedIDs etc.
// let the editor refetch surgically rather than re-pulling the full
// task list (M5.5 may use them; M5 just consumes the success flag).
type reviseResponse struct {
	PlanID     string   `json:"plan_id"`
	Outcome    string   `json:"outcome"`
	NewStatus  string   `json:"new_status"`
	AddedIDs   []string `json:"added_ids,omitempty"`
	RemovedIDs []string `json:"removed_ids,omitempty"`
	UpdatedIDs []string `json:"updated_ids,omitempty"`
}

// reviseAgentPlan handles the JWT-authed revise. Editor consumers
// land here (the UI surface is deferred to M5.5; for now the only
// caller is curl + the AI tool's mTLS twin).
func (s *Service) reviseAgentPlan(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	_, aborted := s.loadPlanForAgent(c, agentID, planID)
	if aborted {
		return
	}

	var ops persistence.RevisionOps
	if err := c.ShouldBindJSON(&ops); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "schema",
			"detail": err.Error(),
		})
		return
	}

	// Empty revise (no adds, no removes, no updates) is a no-op
	// request — surface 400 so the caller knows nothing happened.
	if len(ops.AddTasks) == 0 && len(ops.RemoveTasks) == 0 && len(ops.UpdateTasks) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "empty_revision",
			"detail": "provide at least one of add_tasks, remove_tasks, update_tasks",
		})
		return
	}

	result, err := s.persistence.RevisePlan(c.Request.Context(), planID, ops)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "plan_id": planID}).
			Error("revise plan failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeReviseResponse(c, planID, result)
}

// reviseAgentPlanInternal is the mTLS twin for the executor's
// plan/revise AI tool. Same response shape; same cross-agent
// guard via loadPlanForAgent.
func (s *Service) reviseAgentPlanInternal(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	_, aborted := s.loadPlanForAgent(c, agentID, planID)
	if aborted {
		return
	}

	var ops persistence.RevisionOps
	if err := c.ShouldBindJSON(&ops); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "schema",
			"detail": err.Error(),
		})
		return
	}

	if len(ops.AddTasks) == 0 && len(ops.RemoveTasks) == 0 && len(ops.UpdateTasks) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "empty_revision",
			"detail": "provide at least one of add_tasks, remove_tasks, update_tasks",
		})
		return
	}

	result, err := s.persistence.RevisePlan(c.Request.Context(), planID, ops)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "plan_id": planID}).
			Error("revise plan failed (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeReviseResponse(c, planID, result)
}

func (s *Service) writeReviseResponse(c *gin.Context, planID string, result persistence.RevisionResult) {
	switch result.Outcome {
	case persistence.RevisionOutcomeNotFound:
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "plan_not_found",
			"plan_id": planID,
		})
	case persistence.RevisionOutcomeTerminal:
		c.JSON(http.StatusConflict, gin.H{
			"error":   "plan_terminal",
			"detail":  "plan is completed or cancelled and cannot be revised",
			"plan_id": planID,
		})
	case persistence.RevisionOutcomeInvalid:
		// The detail map carries the structured reason from the
		// persistence-layer validator (e.g. {"reason":"cycle",
		// "task_name":"a"}). Surface verbatim so the AI / editor
		// can route to the offending task.
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "validation",
			"detail": result.ErrorDetail,
		})
	case persistence.RevisionOutcomeRevised:
		c.JSON(http.StatusOK, reviseResponse{
			PlanID:     planID,
			Outcome:    string(result.Outcome),
			NewStatus:  result.NewStatus,
			AddedIDs:   result.AddedIDs,
			RemovedIDs: result.RemovedIDs,
			UpdatedIDs: result.UpdatedIDs,
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "unexpected_outcome",
			"outcome": string(result.Outcome),
		})
	}
}
