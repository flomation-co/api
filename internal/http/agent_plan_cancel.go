package http

// Agent Planning M3 — cancel endpoints + the mTLS twins of M2's
// read endpoint. Three handlers, all related:
//
//   * cancelAgentPlan         — POST /api/v1/agent/:id/plan/:planID/cancel
//                                (JWT — editor cancel button)
//   * cancelAgentPlanInternal — POST /api/v1/internal/agent/:id/plan/:planID/cancel
//                                (mTLS — executor plan/cancel action)
//   * getAgentPlanInternal    — GET  /api/v1/internal/agent/:id/plan/:planID
//                                (mTLS — executor plan/get_status action)
//
// Both internal endpoints STILL verify plan.agent_id == :id. mTLS
// proves "an executor", not "the right executor"; the agent-scope
// guard is the load-bearing protection against a compromised
// executor cancelling another agent's plan.
//
// All three call into the shared loadPlanForAgent helper to keep
// one source of truth for the cross-agent guard + 404 mapping.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
)

// cancelRequest is the wire shape for both cancel endpoints. The
// reason is optional — the editor's confirmation dialog allows
// users to skip typing one, and the AI's plan/cancel action may
// invoke without an explanation.
type cancelRequest struct {
	Reason string `json:"reason"`
}

// cancelResponse mirrors the BlockPlanTask response shape so
// clients can treat both as "outcome-returning" idempotent
// endpoints with the same handling code.
type cancelResponse struct {
	PlanID  string `json:"plan_id"`
	Outcome string `json:"outcome"`
}

// loadPlanForAgent runs the shared lookup chain used by every
// agent-scoped plan endpoint: fetch the plan, map missing → 404,
// map cross-agent → 404 (defence-in-depth, opaque from caller).
// Returns nil + true when the handler has already written a
// response and should return; returns the plan + false on success.
func (s *Service) loadPlanForAgent(c *gin.Context, agentID, planID string) (*api.Plan, bool) {
	plan, err := s.persistence.GetPlanByID(planID)
	if err != nil {
		if err == persistence.ErrPlanNotFound {
			c.AbortWithStatus(http.StatusNotFound)
			return nil, true
		}
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("get plan failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, true
	}
	if plan.AgentID != agentID {
		// Cross-agent guard: the URL declares the agent owner;
		// any mismatch surfaces as opaque 404 rather than 403.
		c.AbortWithStatus(http.StatusNotFound)
		return nil, true
	}
	return plan, false
}

// cancelAgentPlan handles the editor's cancel-button POST. JWT-
// authed; user must canAccessAgent. The HTTP shape mirrors the M2
// read handlers exactly so the editor's request infrastructure
// (api lib, headers) doesn't need a new code path.
func (s *Service) cancelAgentPlan(c *gin.Context) {
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

	var req cancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Empty body is allowed (no reason). Only reject if JSON
		// is malformed.
		if c.Request.ContentLength > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "schema",
				"detail": err.Error(),
			})
			return
		}
	}

	outcome, err := s.persistence.CancelPlan(c.Request.Context(), planID, req.Reason)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("cancel plan failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeCancelResponse(c, planID, outcome)
}

// cancelAgentPlanInternal is the mTLS twin used by the executor's
// plan/cancel action. Same outcome envelope; same cross-agent
// guard; no JWT user.
func (s *Service) cancelAgentPlanInternal(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	_, aborted := s.loadPlanForAgent(c, agentID, planID)
	if aborted {
		return
	}

	var req cancelRequest
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "schema",
			"detail": err.Error(),
		})
		return
	}

	outcome, err := s.persistence.CancelPlan(c.Request.Context(), planID, req.Reason)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("cancel plan failed (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.writeCancelResponse(c, planID, outcome)
}

// writeCancelResponse maps the CancelOutcome to an HTTP status
// code + body. Both 200/cancelled and 200/idempotent are success
// from the caller's perspective; only NotFound returns 404.
func (s *Service) writeCancelResponse(c *gin.Context, planID string, outcome persistence.CancelOutcome) {
	switch outcome {
	case persistence.CancelOutcomeNotFound:
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "plan_not_found",
			"plan_id": planID,
		})
	case persistence.CancelOutcomeCancelled, persistence.CancelOutcomeIdempotent:
		c.JSON(http.StatusOK, cancelResponse{
			PlanID:  planID,
			Outcome: string(outcome),
		})
	default:
		// Defensive — any new outcome value should be surfaced
		// rather than silently mapped to a generic success.
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "unexpected_outcome",
			"outcome": string(outcome),
		})
	}
}

// getAgentPlanInternal is the mTLS twin of M2's getAgentPlan. The
// executor's plan/get_status action calls this to read a plan's
// state without needing a JWT exchange. Same cross-agent guard;
// same {plan, tasks} response shape.
func (s *Service) getAgentPlanInternal(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	plan, aborted := s.loadPlanForAgent(c, agentID, planID)
	if aborted {
		return
	}

	tasks, err := s.persistence.GetPlanTasksByPlanID(planID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("get plan tasks failed (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"plan":  plan,
		"tasks": tasks,
	})
}
