package http

// Internal endpoints that surface prior conversations to the agent
// reasoning loop. Two routes:
//
//   GET  /api/v1/internal/agent/:id/prior-conversations?user_id&limit
//        Called by Launch's inbound dispatch on every message to seed
//        the trigger payload's prior_conversations field.
//
//   POST /api/v1/internal/agent/:id/conversation/:conv_id/messages
//        Called by the executor's agent/get_conversation tool when
//        the LLM wants the full text behind a referenced summary.
//
// Both routes are mTLS-only and rely on the persistence layer to
// enforce (agent_id, agent_user_id) scoping in the SQL itself — see
// agent_prior_conversations.go for the auth posture.

import (
	"errors"
	"net/http"
	"strconv"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// getAgentPriorConversationsInternal handles
// GET /api/v1/internal/agent/:id/prior-conversations
//
// Required query params:
//   user_id — the agent_user_id (conversations are scoped per user)
//
// Optional query params:
//   limit   — cap on the number of summaries (default 5, max 50)
//
// On success: 200 with { "summaries": [...] }.
// On bad input: 400.
// On DB error: 500 (rare; query is small + indexed).
func (s *Service) getAgentPriorConversationsInternal(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	userID := c.Query("user_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id query parameter is required"})
		return
	}

	limit := 5
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > 50 {
				v = 50
			}
			limit = v
		}
	}

	summaries, err := s.persistence.GetRecentPriorConversations(agentID, userID, limit)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"user_id":  userID,
			"error":    err,
		}).Error("failed to fetch prior conversations")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// Coerce nil to empty slice so the JSON shape is stable —
	// downstream code expects an array, never undefined.
	if summaries == nil {
		summaries = []persistence.PriorConversationSummary{}
	}

	c.JSON(http.StatusOK, gin.H{"summaries": summaries})
}

// getAgentConversationMessagesBody is the POST body shape for the
// lookup endpoint. The agent_user_id ride-along is mandatory — the
// persistence layer treats (agent_id, agent_user_id) as the auth
// triple. Sending it in the body rather than the URL keeps the
// path-based route handler clean and lets us extend with optional
// fields later (e.g. iteration range) without breaking compat.
type getAgentConversationMessagesBody struct {
	AgentUserID string `json:"agent_user_id"`
	MaxMessages int    `json:"max_messages"`
}

// getAgentConversationMessagesInternal handles
// POST /api/v1/internal/agent/:id/conversation/:conv_id/messages
//
// Returns the full message history of a conversation, with
// pagination via a `max_messages` cap (default 200, max 500).
// 404 is returned both when the conversation doesn't exist AND
// when it does but isn't accessible to this (agent, user) — see
// the ErrConversationNotAccessible comment for the rationale.
func (s *Service) getAgentConversationMessagesInternal(c *gin.Context) {
	agentID := c.Param("id")
	conversationID := c.Param("conv_id")
	if agentID == "" || conversationID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var body getAgentConversationMessagesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.AgentUserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_user_id is required"})
		return
	}
	if body.MaxMessages <= 0 {
		body.MaxMessages = 200
	}
	if body.MaxMessages > 500 {
		body.MaxMessages = 500
	}

	messages, endedAt, totalCount, wasTruncated, err := s.persistence.GetConversationMessagesForAgent(
		conversationID, agentID, body.AgentUserID, body.MaxMessages,
	)
	if err != nil {
		if errors.Is(err, persistence.ErrConversationNotAccessible) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithFields(log.Fields{
			"agent_id":        agentID,
			"conversation_id": conversationID,
			"user_id":         body.AgentUserID,
			"error":           err,
		}).Error("failed to fetch conversation messages")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"messages":       messages,
		"message_count":  totalCount,
		"ended_at":       endedAt,
		"was_truncated":  wasTruncated,
		"returned_count": len(messages),
	})
}
