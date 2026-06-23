package http

// Agent Planning M2 — SSE stream for plan_event rows. The editor's
// plan-detail page (M2 commit 4) opens an EventSource against this
// route after its initial fetch resolves; each plan_event insert
// the persistence layer commits gets pushed here as it lands.
//
// Auth: gated by streamAuthMiddleware (matches the agent-session
// stream pattern). The editor exchanges its JWT for a short-lived
// opaque token via POST /auth/stream-token then opens the
// EventSource with ?token=<opaque>.
//
// Authorization: agent-scoped — the user must canAccessAgent for
// the URL path's :id, and the plan must belong to that agent.
// 404 (not 403) on a cross-agent plan ID to avoid leaking the
// plan's existence on a different agent.
//
// Keepalive: 15s ticker emits a comment line. Load-bearing for
// reverse proxies that close idle connections — copied verbatim
// from streamAgentSession.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"flomation.app/automate/api/internal/persistence"
)

// streamAgentPlan handles GET /api/v1/agent/:id/plan/:planID/stream.
// Mounted with streamAuthMiddleware ahead of it so the token
// exchange has happened before we reach this handler.
func (s *Service) streamAgentPlan(c *gin.Context) {
	agentID := c.Param("id")
	planID := c.Param("planID")

	// The streamAuthMiddleware sets account_id on the context. From
	// here we replicate the cross-agent + can-access checks from the
	// public read endpoints so the SSE stream carries the same
	// authorisation posture.
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
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if plan.AgentID != agentID {
		// Same defence-in-depth as the read endpoints — opaque 404
		// rather than 403 to avoid leaking cross-agent plan IDs.
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	ch := s.planEventHub.Subscribe(planID)
	defer s.planEventHub.Unsubscribe(planID, ch)

	// Initial connected event so the client knows the stream is
	// established (and the editor can clear a "connecting…" spinner).
	_, _ = fmt.Fprintf(c.Writer, "event: connected\ndata: {\"plan_id\":\"%s\"}\n\n", planID)
	c.Writer.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, event.MarshalSSE())
			c.Writer.Flush()

		case <-ticker.C:
			// Keepalive comment line — proxies and clients see traffic
			// without it counting as an event.
			_, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()

		case <-c.Request.Context().Done():
			return
		}
	}
}
