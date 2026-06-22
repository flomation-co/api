package http

// Agent Planning M1.5 — the AI-facing plan/block endpoint. Mounted
// on the internal-mTLS engine. The only legitimate caller is the
// executor's plan/block action, which the AI tool loop invokes when
// the model decides it cannot make progress on the current task.
//
// The endpoint is intentionally small. Validation is shallow because
// the failure modes are bounded: the AI either passes a valid task ID
// (in which case we transition + audit) or it passes a junk one (in
// which case the API returns 404 and the action surfaces the message
// for the model to read and self-correct).
//
// Idempotency: BlockPlanTask returns BlockOutcomeIdempotent when the
// task is already terminal. The handler still returns 200 — the AI
// shouldn't be punished for calling plan/block twice across a retry,
// and the action's tool_result reflects "already blocked" so the
// model sees the actual state.

import (
	"net/http"
	"strings"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// blockPlanTaskRequest is the wire shape of the request body. Just
// the reason — the URL carries the task ID.
type blockPlanTaskRequest struct {
	Reason string `json:"reason"`
}

// blockPlanTaskResponse is the success shape — the outcome tells the
// AI whether this call transitioned the task or matched an already-
// terminal state.
type blockPlanTaskResponse struct {
	PlanTaskID string `json:"plan_task_id"`
	Outcome    string `json:"outcome"`
}

// blockPlanTask handles POST /api/v1/internal/plan_task/:planTaskID/block.
// mTLS-only — registered on internalRouter.
func (s *Service) blockPlanTask(c *gin.Context) {
	planTaskID := c.Param("planTaskID")
	if planTaskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "planTaskID required"})
		return
	}

	var req blockPlanTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "schema",
			"detail": err.Error(),
		})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason required"})
		return
	}

	outcome, err := s.persistence.BlockPlanTask(c.Request.Context(), planTaskID, reason)
	if err != nil {
		log.WithFields(log.Fields{
			"plan_task_id": planTaskID,
			"error":        err,
		}).Error("plan/block failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	switch outcome {
	case persistence.BlockOutcomeNotFound:
		// Surface as 404 so the action can render a clean error for
		// the AI ("unknown plan task; check the ID and retry"). The
		// model can correct on a subsequent turn or set_output with a
		// fallback summary.
		c.JSON(http.StatusNotFound, gin.H{
			"error":        "plan_task_not_found",
			"plan_task_id": planTaskID,
		})
		return
	case persistence.BlockOutcomeIdempotent, persistence.BlockOutcomeBlocked:
		c.JSON(http.StatusOK, blockPlanTaskResponse{
			PlanTaskID: planTaskID,
			Outcome:    string(outcome),
		})
		return
	default:
		// Defensive — any new outcome value should be surfaced rather
		// than silently mapped to 200.
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "unexpected_outcome",
			"outcome": string(outcome),
		})
	}
}
