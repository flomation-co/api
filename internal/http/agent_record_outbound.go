package http

import (
	"net/http"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// recordAgentOutboundRequest is the POST body the executor's flow
// engine sends after a successful AI-tool-driven message send. The
// engine derives every field from the matched tool node's inputs,
// outputs, and the AI execution's ExecutionContext.
//
//   - ChannelType: derived from the matched action's category label
//     ("slack", "telegram", "teams", "discord", ...). Already
//     normalised (no sub-type suffixes — `telegram_voice` collapses
//     to `telegram`).
//
//   - ChannelID: the destination channel id (DM: the recipient's user
//     id; group: the channel id). The same field used by the inbound
//     resolver for conversation scoping, so a future inbound from the
//     same recipient lands in the same conversation row.
//
//   - RecipientID: the recipient's stable channel-specific user id
//     when delivering a DM. Drives identity resolution into
//     agent_identity → agent_user. Empty when the message went to a
//     multi-user channel — the engine populates only ChannelID in
//     that case, and the persisted message lands on the conversation
//     with agent_user_id NULL (channel-scoped, not user-scoped).
//
//   - Content: the message body, as sent. The engine reads this from
//     the tool node's resolved inputs (the standard `content` /
//     `message` input on every messaging action).
//
//   - SourceConversationID: the conversation the orchestrator was
//     responding to when the relay was dispatched. Optional; populates
//     agent_message.source_conversation_id for audit.
type recordAgentOutboundRequest struct {
	ChannelType          string  `json:"channel_type" binding:"required"`
	ChannelID            string  `json:"channel_id" binding:"required"`
	RecipientID          string  `json:"recipient_id,omitempty"`
	Content              string  `json:"content" binding:"required"`
	SourceConversationID *string `json:"source_conversation_id,omitempty"`
}

// recordAgentOutboundInternal handles
// POST /api/v1/internal/agent/:id/record-outbound.
//
// Composes the existing inbound primitives — identity resolution,
// conversation resolution, message insertion — but in service of a
// proactive outbound rather than a reactive inbound. The recipient
// gets a real conversation row and a real agent_message; next time
// they message the agent, conversation_history surfaces the relay
// naturally and the model can pick up the thread.
//
// Failure semantics: any DB-level error returns 500 so the caller
// (the executor's flow engine) can log it. Personal-mode anonymous-
// stub rejection (CHECK constraint on users.is_anonymous) and
// missing-recipient on a DM-shaped channel resolve to a 200 with a
// "skipped" reason — those aren't bugs, they're "we just can't
// associate this message with a conversation". The send itself
// already succeeded; the executor doesn't need to know.
func (s *Service) recordAgentOutboundInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body recordAgentOutboundRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Resolve the recipient identity → agent_user when the channel
	// delivered a DM. For multi-user channels (channel_id without an
	// associated user id), the conversation is recorded without a
	// user binding — agent_user_id NULL. Personal-mode + an
	// undeclared recipient short-circuits to "skipped" because we
	// can't create anonymous stubs in personal mode.
	var agentUserID *string
	if body.RecipientID != "" {
		identity, user, err := s.persistence.ResolveOrCreateAgentIdentity(
			agentID, agent.OrganisationID, body.ChannelType, body.RecipientID, nil, nil,
		)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id":     agentID,
				"channel_type": body.ChannelType,
				"recipient_id": body.RecipientID,
				"error":        err,
			}).Warn("record-outbound: identity resolution failed; falling back to channel-scoped conversation")
		} else if identity != nil && user != nil {
			id := user.ID
			agentUserID = &id
		}
		// identity == nil with no error happens in personal mode
		// when the recipient has no declared identity — the
		// resolver returns (nil, nil) rather than upserting a stub
		// (CHECK constraint forbids). Treat as channel-scoped.
	}

	conv, err := s.persistence.ResolveOrCreateAgentConversation(
		agentID, agentUserID, body.ChannelType, body.ChannelID, nil,
		agent.IdleTimeoutSeconds,
	)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":     agentID,
			"channel_type": body.ChannelType,
			"channel_id":   body.ChannelID,
			"error":        err,
		}).Error("record-outbound: conversation resolution failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if conv == nil || conv.Conversation == nil {
		c.JSON(http.StatusOK, gin.H{"skipped": "no conversation resolved"})
		return
	}

	conversationID := conv.Conversation.ID
	msg := api.AgentMessage{
		AgentID:              agentID,
		ConversationID:       &conversationID,
		Direction:            "outbound",
		ChannelType:          body.ChannelType,
		Content:              body.Content,
		SourceConversationID: body.SourceConversationID,
	}
	id, err := s.persistence.CreateAgentMessageInConversation(msg)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":        agentID,
			"conversation_id": conversationID,
			"error":           err,
		}).Error("record-outbound: message insert failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message_id":      *id,
		"conversation_id": conversationID,
		"channel_scoped":  agentUserID == nil,
	})
}
