package http

// Phase 2 of the Agent Memory feature — internal HTTP handlers for the
// three new tables introduced in migration 42:
//
//   - agent_memory         (durable facts and preferences)
//   - agent_pending_action (natural-language intents awaiting confirmation)
//   - agent_commitment     (promises awaiting time or signal)
//
// All routes live under /api/v1/internal/ and are intentionally NOT behind
// JWT — they are called by Launch and by the executor's agent/remember,
// agent/recall, agent/forget actions over the internal network, the same
// way Phase 1's internal agent routes are reached.
//
// Route summary (registered in service.go):
//
//   POST   /internal/agent/:id/memory             create memory
//   GET    /internal/agent/:id/memory             list memories for a user
//   GET    /internal/memory/:id                   fetch one memory
//   DELETE /internal/memory/:id                   delete a memory
//
//   POST   /internal/agent/:id/pending-action     create pending action
//   GET    /internal/agent/:id/pending-action     list open pending actions
//   PATCH  /internal/pending-action/:id           update status
//
//   POST   /internal/agent/:id/commitment         create commitment
//   GET    /internal/commitment/due               poller — list due commitments
//   GET    /internal/agent/:id/commitment         list a user's commitments
//   PATCH  /internal/commitment/:id               update status
//
// Error responses follow the Phase 1 convention: 404 when the agent is
// unknown, 400 for malformed input, 500 on persistence errors.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// --- Agent Memory ---

// createAgentMemoryInternalRequest is the POST body for writing a memory.
// agent_user_id is optional — when omitted, the memory is stored with
// scope='global' (an agent-wide fact rather than a per-user one).
type createAgentMemoryInternalRequest struct {
	AgentUserID        *string    `json:"agent_user_id,omitempty"`
	Scope              string     `json:"scope" binding:"required"` // 'user' | 'global'
	MemoryType         string     `json:"memory_type" binding:"required"`
	Title              string     `json:"title" binding:"required"`
	Body               string     `json:"body" binding:"required"`
	SourceConversation *string    `json:"source_conversation,omitempty"`
	SourceMessage      *string    `json:"source_message,omitempty"`
	Confidence         *float64   `json:"confidence,omitempty"`
	Pinned             bool       `json:"pinned,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

// createAgentMemoryInternal handles POST /api/v1/internal/agent/:id/memory.
func (s *Service) createAgentMemoryInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body createAgentMemoryInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Confidence defaults to 1.0 for manual writes. The extraction
	// pipeline passes an explicit value; direct flow-author writes via
	// agent/remember don't need to think about it.
	confidence := 1.0
	if body.Confidence != nil {
		confidence = *body.Confidence
	}

	id, err := s.persistence.CreateAgentMemory(api.AgentMemory{
		AgentID:            agentID,
		AgentUserID:        body.AgentUserID,
		Scope:              body.Scope,
		MemoryType:         body.MemoryType,
		Title:              body.Title,
		Body:               body.Body,
		SourceConversation: body.SourceConversation,
		SourceMessage:      body.SourceMessage,
		Confidence:         confidence,
		Pinned:             body.Pinned,
		ExpiresAt:          body.ExpiresAt,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to create agent memory (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *id})
}

// listAgentMemoriesInternal handles GET /api/v1/internal/agent/:id/memory.
//
// Query parameters:
//   - agent_user_id (required): the user whose memories to fetch
//   - pinned (optional): "true" to restrict to pinned rows only
//   - limit  (optional): max rows, default 100
func (s *Service) listAgentMemoriesInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	agentUserID := c.Query("agent_user_id")
	if agentUserID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "agent_user_id query parameter is required",
		})
		return
	}

	pinnedOnly := c.Query("pinned") == "true"
	limit := 100
	if v := c.Query("limit"); v != "" {
		if parsed, perr := strconv.Atoi(v); perr == nil && parsed > 0 {
			limit = parsed
		}
	}
	// Clamp to a hard ceiling so a malicious or buggy caller can't ask
	// for a million rows.
	if limit > 1000 {
		limit = 1000
	}

	memories, err := s.persistence.GetAgentMemoriesForUser(agentUserID, pinnedOnly, limit)
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_id":      agentID,
			"agent_user_id": agentUserID,
		}).Error("unable to list agent memories (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if memories == nil {
		memories = []*api.AgentMemory{}
	}

	c.JSON(http.StatusOK, memories)
}

// getAgentMemoryInternal handles GET /api/v1/internal/memory/:id.
func (s *Service) getAgentMemoryInternal(c *gin.Context) {
	id := c.Param("id")

	mem, err := s.persistence.GetAgentMemoryByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error":     err,
			"memory_id": id,
		}).Error("unable to fetch agent memory (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if mem == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, mem)
}

// deleteAgentMemoryInternal handles DELETE /api/v1/internal/memory/:id.
func (s *Service) deleteAgentMemoryInternal(c *gin.Context) {
	id := c.Param("id")

	if err := s.persistence.DeleteAgentMemory(id); err != nil {
		log.WithFields(log.Fields{
			"error":     err,
			"memory_id": id,
		}).Error("unable to delete agent memory (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Pending Actions ---

type createAgentPendingActionInternalRequest struct {
	AgentUserID        string          `json:"agent_user_id" binding:"required"`
	Type               string          `json:"type" binding:"required"`
	Payload            json.RawMessage `json:"payload,omitempty"`
	Evidence           string          `json:"evidence" binding:"required"`
	SourceConversation *string         `json:"source_conversation,omitempty"`
	SourceMessage      *string         `json:"source_message,omitempty"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
}

// createAgentPendingActionInternal handles POST /api/v1/internal/agent/:id/pending-action.
func (s *Service) createAgentPendingActionInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body createAgentPendingActionInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	id, err := s.persistence.CreateAgentPendingAction(api.AgentPendingAction{
		AgentID:            agentID,
		AgentUserID:        body.AgentUserID,
		Type:               body.Type,
		Payload:            body.Payload,
		Evidence:           body.Evidence,
		SourceConversation: body.SourceConversation,
		SourceMessage:      body.SourceMessage,
		ExpiresAt:          body.ExpiresAt,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to create pending action (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *id})
}

// listOpenPendingActionsInternal handles GET /api/v1/internal/agent/:id/pending-action.
//
// Returns only open pending actions (awaiting_confirmation or
// confirmed_here_awaiting_other_side). Query parameter:
//   - agent_user_id (required)
func (s *Service) listOpenPendingActionsInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	agentUserID := c.Query("agent_user_id")
	if agentUserID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "agent_user_id query parameter is required",
		})
		return
	}

	actions, err := s.persistence.GetOpenPendingActionsForUser(agentUserID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_id":      agentID,
			"agent_user_id": agentUserID,
		}).Error("unable to list pending actions (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if actions == nil {
		actions = []*api.AgentPendingAction{}
	}

	c.JSON(http.StatusOK, actions)
}

type updatePendingActionInternalRequest struct {
	Status string `json:"status" binding:"required"`
}

// updatePendingActionStatusInternal handles PATCH /api/v1/internal/pending-action/:id.
func (s *Service) updatePendingActionStatusInternal(c *gin.Context) {
	id := c.Param("id")

	var body updatePendingActionInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdatePendingActionStatus(id, body.Status); err != nil {
		log.WithFields(log.Fields{
			"error":             err,
			"pending_action_id": id,
		}).Error("unable to update pending action status (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Commitments ---

type createAgentCommitmentInternalRequest struct {
	AgentUserID        *string         `json:"agent_user_id,omitempty"`
	ConversationID     *string         `json:"conversation_id,omitempty"`
	Kind               string          `json:"kind" binding:"required"`
	Description        string          `json:"description" binding:"required"`
	Payload            json.RawMessage `json:"payload,omitempty"`
	TriggerType        string          `json:"trigger_type" binding:"required"`
	DueAt              *time.Time      `json:"due_at,omitempty"`
	Condition          json.RawMessage `json:"condition,omitempty"`
	SourceConversation *string         `json:"source_conversation,omitempty"`
	SourceMessage      *string         `json:"source_message,omitempty"`
	MadeBy             string          `json:"made_by,omitempty"` // 'assistant' | 'user'; defaults to 'assistant'
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
}

// createAgentCommitmentInternal handles POST /api/v1/internal/agent/:id/commitment.
func (s *Service) createAgentCommitmentInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body createAgentCommitmentInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	id, err := s.persistence.CreateAgentCommitment(api.AgentCommitment{
		AgentID:            agentID,
		AgentUserID:        body.AgentUserID,
		ConversationID:     body.ConversationID,
		Kind:               body.Kind,
		Description:        body.Description,
		Payload:            body.Payload,
		TriggerType:        body.TriggerType,
		DueAt:              body.DueAt,
		Condition:          &body.Condition,
		SourceConversation: body.SourceConversation,
		SourceMessage:      body.SourceMessage,
		MadeBy:             body.MadeBy,
		ExpiresAt:          body.ExpiresAt,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to create commitment (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *id})
}

// listDueCommitmentsInternal handles GET /api/v1/internal/commitment/due.
//
// This is the hot path that the Phase 3 commitment poller will hit on
// its 30-second cadence. It returns only commitments with status='pending'
// and due_at <= NOW(), oldest-first. Ships in Phase 2a so Phase 3 has a
// stable endpoint to call.
//
// Query parameters:
//   - limit (optional): max rows, default 100, cap 1000
func (s *Service) listDueCommitmentsInternal(c *gin.Context) {
	limit := 100
	if v := c.Query("limit"); v != "" {
		if parsed, perr := strconv.Atoi(v); perr == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	commitments, err := s.persistence.GetDueCommitments(limit)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to list due commitments (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if commitments == nil {
		commitments = []*api.AgentCommitment{}
	}

	c.JSON(http.StatusOK, commitments)
}

// listCommitmentsForUserInternal handles GET /api/v1/internal/agent/:id/commitment.
//
// Lists every commitment belonging to a user (all statuses), newest first.
// Used by Phase 6's profile page. Included in Phase 2a so the CRUD surface
// is complete from day one.
func (s *Service) listCommitmentsForUserInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	agentUserID := c.Query("agent_user_id")
	if agentUserID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "agent_user_id query parameter is required",
		})
		return
	}

	limit := 100
	if v := c.Query("limit"); v != "" {
		if parsed, perr := strconv.Atoi(v); perr == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	commitments, err := s.persistence.GetCommitmentsForUser(agentUserID, limit)
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_id":      agentID,
			"agent_user_id": agentUserID,
		}).Error("unable to list commitments for user (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if commitments == nil {
		commitments = []*api.AgentCommitment{}
	}

	c.JSON(http.StatusOK, commitments)
}

type updateCommitmentInternalRequest struct {
	Status string `json:"status" binding:"required"`
}

// updateCommitmentStatusInternal handles PATCH /api/v1/internal/commitment/:id.
func (s *Service) updateCommitmentStatusInternal(c *gin.Context) {
	id := c.Param("id")

	var body updateCommitmentInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateCommitmentStatus(id, body.Status); err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"commitment_id": id,
		}).Error("unable to update commitment status (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusNoContent)
}
