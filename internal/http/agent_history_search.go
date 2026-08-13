package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type searchAgentHistoryRequest struct {
	AgentUserID string `json:"agent_user_id"`
	Query       string `json:"query"`
	Limit       int    `json:"limit"`
}

// searchAgentHistoryInternal handles POST /api/v1/internal/agent/:id/history/search.
//
// Full-text search over the agent's ENTIRE conversation history with one user —
// across all channels and conversations — so an agent can recall an earlier turn
// after its context window has been compacted. Scoped strictly by
// (agent_id, agent_user_id); an unknown agent is 404 and a missing/empty scope
// yields no results, so nothing leaks about another user's or agent's history.
func (s *Service) searchAgentHistoryInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body searchAgentHistoryRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	results, err := s.persistence.SearchAgentMessages(agentID, body.AgentUserID, body.Query, body.Limit)
	if err != nil {
		log.WithError(err).Error("agent history search failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}
