package http

import (
	"net/http"
	"strings"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	pgvector "github.com/pgvector/pgvector-go"
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

	// Full-text search is always run — it's instant (generated tsvector) and
	// covers messages the embedding backfill hasn't reached yet.
	fts, err := s.persistence.SearchAgentMessages(agentID, body.AgentUserID, body.Query, body.Limit)
	if err != nil {
		log.WithError(err).Error("agent history search (fts) failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}

	// Hybrid: when an embedding provider is configured, add semantic recall and
	// fuse the two rankings with RRF. Any embedding/semantic failure degrades
	// gracefully to the FTS results — never fails the request.
	results := fts
	if s.embeddingProvider != nil && strings.TrimSpace(body.Query) != "" && body.AgentUserID != "" {
		if vec, eerr := s.embeddingProvider.Embed(c.Request.Context(), body.Query); eerr == nil && len(vec) > 0 {
			sem, serr := s.persistence.SearchAgentMessagesByEmbedding(agentID, body.AgentUserID, pgvector.NewVector(vec), body.Limit)
			if serr != nil {
				log.WithError(serr).Warn("agent history search (semantic) failed; using FTS only")
			} else {
				results = persistence.FuseByRRF([][]persistence.AgentMessageSearchResult{fts, sem}, body.Limit)
			}
		} else if eerr != nil {
			log.WithError(eerr).Warn("agent history search: query embedding failed; using FTS only")
		}
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}
