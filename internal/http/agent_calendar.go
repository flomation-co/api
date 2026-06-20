package http

// Internal endpoint that surfaces an agent_user's upcoming Google
// Calendar events to the executor's agent/get_calendar tool.
//
// Architecture: tool, not auto-context. The agent calls this when
// the conversation makes it relevant (the user mentions where they
// are, asks about the day, says they're running late, etc.). We
// deliberately don't fold the calendar into the system prompt or
// the trigger payload — agents pay no token cost when calendar
// awareness isn't needed.
//
// Auth: the calendar credential is owned by the agent_user, so the
// route is keyed on (agent_id, agent_user_id). The credential
// itself never leaves the API. Even the executor only receives the
// rendered events list.

import (
	"context"
	"net/http"
	"time"

	"flomation.app/automate/api/internal/agent"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// agentCalendarRequest is the POST body shape from the executor's
// agent/get_calendar tool. agent_user_id is mandatory — calendar
// data is per-user. Hours bounds how far ahead we look (1..168,
// default 24).
type agentCalendarRequest struct {
	AgentUserID string `json:"agent_user_id"`
	Hours       int    `json:"hours"`
}

// calendarFetchTimeout caps the total time the API will wait on a
// Google response before returning an empty list with an error
// note. The schedule cache covers warm hits in microseconds; this
// only matters on cache miss.
const calendarFetchTimeout = 8 * time.Second

// calendarCache is a single package-level cache so calls across
// executions for the same agent_user share entries within the TTL
// window. Holding it on the http package keeps the handler
// dependency surface narrow.
var calendarCache = agent.NewScheduleCache()

// getAgentCalendarEventsInternal handles
// POST /api/v1/internal/agent/:id/calendar/events
//
// Returns: { events: [...], event_count: N } on success.
// Returns: { events: [], event_count: 0, no_calendar: true } when
// the user hasn't linked a calendar — the executor surfaces this as
// a clear tool result rather than an error, so the agent can fall
// back to asking the user directly.
func (s *Service) getAgentCalendarEventsInternal(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var body agentCalendarRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.AgentUserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_user_id is required"})
		return
	}
	if body.Hours <= 0 {
		body.Hours = 24
	}
	if body.Hours > 168 {
		body.Hours = 168
	}

	// Pull the (refresh-poller-rotated) calendar access token for
	// this user. Empty token means "no calendar linked" — return
	// the structured no_calendar response rather than a 404 so the
	// tool can offer the agent a meaningful next step.
	token, err := s.persistence.GetAgentUserCalendarAccessToken(body.AgentUserID)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":      agentID,
			"agent_user_id": body.AgentUserID,
			"error":         err,
		}).Error("agent calendar: credential lookup failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if token == "" {
		c.JSON(http.StatusOK, gin.H{
			"events":      []agent.ScheduleEvent{},
			"event_count": 0,
			"no_calendar": true,
		})
		return
	}

	// The schedule cache wraps the fetch in a 5-min TTL. Concurrent
	// requests for the same key share one Google call.
	ctx, cancel := context.WithTimeout(c.Request.Context(), calendarFetchTimeout)
	defer cancel()
	events, err := calendarCache.GetEvents(ctx, body.AgentUserID, token, body.Hours)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":      agentID,
			"agent_user_id": body.AgentUserID,
			"hours":         body.Hours,
			"error":         err,
		}).Warn("agent calendar: fetch failed")
		c.JSON(http.StatusOK, gin.H{
			"events":      []agent.ScheduleEvent{},
			"event_count": 0,
			"error":       err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"events":      events,
		"event_count": len(events),
	})
}
