package http

// Agent Planning M2 — the editor-facing plan read endpoints.
// Mounted on the public v1 router (JWT auth + canAccessAgent gating)
// so the Plans tab on agent-detail can list, drill into, and tail
// the event timeline for plans the user can see.
//
// Three endpoints, agent-scoped:
//
//   - GET /agent/:id/plan                — list plans for the agent
//   - GET /agent/:id/plan/:planID        — plan header + tasks
//   - GET /agent/:id/plan/:planID/event  — event timeline (paginated)
//
// Live updates (SSE stream of plan_event rows as they're inserted)
// land in M2 commit 3 — this commit is the read-only foundation.
//
// Validation chain shared by all three handlers:
//
//   1. jwtMiddleware has populated account_id + organisation_id
//   2. getUserFromContext resolves the user struct
//   3. GetAgentByID confirms the agent exists (404 otherwise)
//   4. canAccessAgent enforces the agent-scoped permission gate
//      (same gate sessions/state/memory tabs use)
//   5. For the plan-detail + events endpoints, the plan's agent_id
//      must match the URL path's agent id — defends against an
//      attacker swapping in a planID belonging to a different
//      agent the user CAN see.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api/internal/persistence"
)

// getAgentPlans handles GET /api/v1/agent/:id/plan. Returns the page
// of plans for the agent (newest-first), with x-total-items set for
// client-side pagination. Optional ?status=active filter.
func (s *Service) getAgentPlans(c *gin.Context) {
	agentID := c.Param("id")
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

	limit, offset := parsePagination(c)
	statusFilter := c.Query("status")

	plans, total, err := s.persistence.ListPlansByAgentID(agentID, statusFilter, limit, offset)
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("list plans failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Writer.Header().Set("x-total-items", strconv.Itoa(total))
	c.JSON(http.StatusOK, plans)
}

// getAgentPlan handles GET /api/v1/agent/:id/plan/:planID. Returns
// the plan header bundled with its full task list. The task list
// is sorted oldest-first (matching dispatch order semantics, see
// GetPlanTasksByPlanID's comment).
func (s *Service) getAgentPlan(c *gin.Context) {
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

	plan, err := s.persistence.GetPlanByID(planID)
	if err != nil {
		if err == persistence.ErrPlanNotFound {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("get plan failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Defence-in-depth: the URL path declares which agent this plan
	// "belongs to" via canAccessAgent. If the plan_id actually belongs
	// to a different agent the user cannot see, surface 404 rather
	// than leak the existence of the cross-agent plan.
	if plan.AgentID != agentID {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	tasks, err := s.persistence.GetPlanTasksByPlanID(planID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("get plan tasks failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Slim the response: we ship as { plan, tasks } so the editor
	// can render header + task list from a single fetch. Future M3
	// surfaces (recent events inlined, agent metadata) bolt on
	// without breaking the existing keys.
	c.JSON(http.StatusOK, gin.H{
		"plan":  plan,
		"tasks": tasks,
	})
}

// getAgentPlanEvents handles GET /api/v1/agent/:id/plan/:planID/event.
// Returns up to `limit` events newest-first; the editor passes the
// timestamp of its oldest currently-displayed event as `?before=` to
// page backwards through history.
func (s *Service) getAgentPlanEvents(c *gin.Context) {
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

	// Cross-agent guard: confirm the plan belongs to this agent.
	plan, err := s.persistence.GetPlanByID(planID)
	if err != nil {
		if err == persistence.ErrPlanNotFound {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("get plan for events failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if plan.AgentID != agentID {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	limit, _ := parsePagination(c)
	var before *time.Time
	if raw := c.Query("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "before must be RFC3339",
				"detail": err.Error(),
			})
			return
		}
		before = &t
	}

	events, err := s.persistence.ListPlanEventsByPlanID(planID, limit, before)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"plan_id": planID,
		}).Error("list plan events failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, events)
}
