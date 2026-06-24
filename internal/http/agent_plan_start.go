package http

// Agent Planning M4 — plan/start endpoints. Three handlers, parallel
// to M3's cancel surface:
//
//   * startAgentPlan         — POST /api/v1/agent/:id/plan/:planID/start
//                              (JWT — editor Start button)
//   * startAgentPlanInternal — POST /api/v1/internal/agent/:id/plan/:planID/start
//                              (mTLS — executor plan/start action)
//
// Both endpoints reuse the M3 loadPlanForAgent helper for the
// agent-scope guard + 404 mapping. Body is empty (no parameters
// needed — POST is the verb that triggers the transition).
//
// Outcome → HTTP status:
//   started          → 200
//   idempotent       → 200 (already active)
//   not_found        → 404
//   already_terminal → 409 (cancelled or completed — can't resurrect)

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api/internal/persistence"
)

// startResponse is the wire shape returned by both start endpoints.
// Mirrors the cancel response so the editor + executor's handling
// code can be near-identical.
type startResponse struct {
	PlanID  string `json:"plan_id"`
	Outcome string `json:"outcome"`
}

// startAgentPlan handles the editor's Start-button POST. JWT-authed;
// user must canAccessAgent for the URL path's :id.
func (s *Service) startAgentPlan(c *gin.Context) {
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

	outcome, err := s.persistence.StartPlan(c.Request.Context(), planID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("start plan failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeStartResponse(c, planID, outcome)
}

// startAgentPlanInternal is the mTLS twin used by the executor's
// plan/start action. Same outcome envelope; same cross-agent guard;
// no JWT user.
func (s *Service) startAgentPlanInternal(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	_, aborted := s.loadPlanForAgent(c, agentID, planID)
	if aborted {
		return
	}

	outcome, err := s.persistence.StartPlan(c.Request.Context(), planID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("start plan failed (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeStartResponse(c, planID, outcome)
}

// writeStartResponse maps the StartOutcome to an HTTP status + body.
// already_terminal is a 409 (Conflict) rather than 200 because the
// caller wanted to start something that can never be started — the
// editor should surface this as an error, and the AI tool should
// not treat it as success.
func (s *Service) writeStartResponse(c *gin.Context, planID string, outcome persistence.StartOutcome) {
	switch outcome {
	case persistence.StartOutcomeNotFound:
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "plan_not_found",
			"plan_id": planID,
		})
	case persistence.StartOutcomeAlreadyTerminal:
		c.JSON(http.StatusConflict, gin.H{
			"error":   "plan_already_terminal",
			"detail":  "plan is completed, cancelled, or blocked — cannot be started",
			"plan_id": planID,
		})
	case persistence.StartOutcomeStarted, persistence.StartOutcomeIdempotent:
		c.JSON(http.StatusOK, startResponse{
			PlanID:  planID,
			Outcome: string(outcome),
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "unexpected_outcome",
			"outcome": string(outcome),
		})
	}
}
