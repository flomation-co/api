package http

// Phase 1 of the Agent Memory feature — HTTP handlers for the internal
// (service-to-service, no-auth) endpoints Launch calls on the incoming
// webhook path. See plans/agent_memory.md.
//
// These endpoints exist alongside the existing internal agent endpoints
// in agent.go (createAgentMessageInternal, getAgentStateInternal, etc.)
// and are registered in service.go under the same internal route group.
//
// None of these handlers are reachable via a JWT-authenticated browser
// path. They are called only by Launch over the internal network.

import (
	"net/http"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// resolveAgentIdentityInternalRequest is the POST body for identity
// resolution. Only channel_type and channel_external_id are required;
// everything else is optional and supplies initial metadata on first
// contact.
type resolveAgentIdentityInternalRequest struct {
	ChannelType       string  `json:"channel_type" binding:"required"`
	ChannelExternalID string  `json:"channel_external_id" binding:"required"`
	ChannelScope      *string `json:"channel_scope,omitempty"`
	DisplayName       *string `json:"display_name,omitempty"`
}

// resolveAgentIdentityInternalResponse returns the resolved identity
// along with its canonical AgentUser. Launch uses the agent_user_id to
// scope subsequent conversation resolution and memory lookups.
type resolveAgentIdentityInternalResponse struct {
	Identity *api.AgentIdentity `json:"identity"`
	User     *api.AgentUser     `json:"user"`
}

// resolveAgentIdentityInternal handles POST /api/v1/internal/agent/:id/resolve-identity.
//
// Looks up the identity keyed on (channel_type, channel_external_id,
// channel_scope) and returns it with its AgentUser. On first contact
// with an unrecognised external identifier, auto-creates both the user
// and the identity (unverified).
func (s *Service) resolveAgentIdentityInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body resolveAgentIdentityInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	identity, user, err := s.persistence.ResolveOrCreateAgentIdentity(
		agentID,
		agent.OrganisationID,
		body.ChannelType,
		body.ChannelExternalID,
		body.ChannelScope,
		body.DisplayName,
	)
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to resolve agent identity (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, resolveAgentIdentityInternalResponse{
		Identity: identity,
		User:     user,
	})
}

// resolveAgentConversationInternalRequest is the POST body for opening
// or continuing a conversation. agent_user_id is optional — if omitted,
// the conversation is created unassociated and can be linked later.
type resolveAgentConversationInternalRequest struct {
	AgentUserID *string `json:"agent_user_id,omitempty"`
	ChannelType string  `json:"channel_type" binding:"required"`
	ChannelID   string  `json:"channel_id" binding:"required"`
	ThreadID    *string `json:"thread_id,omitempty"`
}

// resolveAgentConversationInternal handles POST /api/v1/internal/agent/:id/conversation.
//
// Returns the open conversation for the given (agent, channel, thread)
// or creates a new one.
func (s *Service) resolveAgentConversationInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body resolveAgentConversationInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	conv, err := s.persistence.ResolveOrCreateAgentConversation(
		agentID,
		body.AgentUserID,
		body.ChannelType,
		body.ChannelID,
		body.ThreadID,
	)
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to resolve agent conversation (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, conv)
}

// getAgentConversationHistoryInternal handles GET /api/v1/internal/conversation/:id/history.
//
// Returns the last N messages for a conversation in chronological order
// (oldest first), suitable for direct consumption by the AI action's
// conversation_history input.
//
// Query parameters:
//   - limit: max number of messages to return (default 20, max 200)
func (s *Service) getAgentConversationHistoryInternal(c *gin.Context) {
	conversationID := c.Param("id")

	limit, _ := parsePagination(c)
	// The standard parsePagination defaults to 50; for conversation history
	// the AI-sided default is 20 turns, so honour limit= explicitly and
	// clamp.
	if limitQuery := c.Query("limit"); limitQuery == "" {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	messages, err := s.persistence.GetAgentConversationMessages(conversationID, limit)
	if err != nil {
		log.WithFields(log.Fields{
			"error":           err,
			"conversation_id": conversationID,
		}).Error("unable to fetch conversation history (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []*api.AgentMessage{}
	}

	c.JSON(http.StatusOK, messages)
}

// createAgentConversationMessageInternalRequest is the POST body for
// recording a message into a specific conversation. This is the
// conversation-scoped sibling of createAgentMessageInternal and
// supersedes it on the agent memory path. The old endpoint is kept for
// backwards compatibility with any pre-Phase-1 callers.
type createAgentConversationMessageInternalRequest struct {
	Direction   string      `json:"direction" binding:"required"`
	ChannelType string      `json:"channel_type" binding:"required"`
	Sender      *string     `json:"sender,omitempty"`
	Content     string      `json:"content" binding:"required"`
	Metadata    interface{} `json:"metadata,omitempty"`
	ExecutionID *string     `json:"execution_id,omitempty"`
}

// createAgentConversationMessageInternal handles POST /api/v1/internal/conversation/:id/message.
//
// Writes a conversation-scoped agent_message with an auto-assigned
// sequence number. This is what Launch calls (Phase 1.4) when it needs
// to record an inbound user turn OR what the AI action calls (via the
// existing RecordAssistantReply helper, once it's pointed at this
// endpoint in a later task) to record an outbound assistant reply.
func (s *Service) createAgentConversationMessageInternal(c *gin.Context) {
	conversationID := c.Param("id")

	// We also need the agent_id to fill in on the message row. Ideally the
	// caller would pass it, but we can also resolve it from the conversation
	// itself for a slightly chattier API at the cost of one extra fetch.
	// We take the "pass agent_id in the URL segment" route via a header
	// or body field to keep the handler deterministic.
	var body createAgentConversationMessageInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// agent_id is required on the message row. The persistence layer does
	// not currently expose a lookup-by-id for AgentConversation (the
	// natural-key lookup method requires the full tuple), and Launch
	// already holds the agent_id locally when it makes this call, so the
	// simplest approach is a query parameter. This keeps the handler
	// stateless and avoids an extra round-trip to the DB just to look up
	// a value the caller already knows.
	agentID := c.Query("agent_id")
	if agentID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "agent_id query parameter is required",
		})
		return
	}

	msgID, err := s.persistence.CreateAgentMessageInConversation(api.AgentMessage{
		AgentID:        agentID,
		ConversationID: &conversationID,
		Direction:      body.Direction,
		ChannelType:    body.ChannelType,
		Sender:         body.Sender,
		Content:        body.Content,
		Metadata:       body.Metadata,
		ExecutionID:    body.ExecutionID,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error":           err,
			"conversation_id": conversationID,
			"agent_id":        agentID,
		}).Error("unable to create conversation message (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *msgID})
}
