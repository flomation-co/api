package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/agent/persistence"
	log "github.com/sirupsen/logrus"
)

const defaultHistoryLimit = 30

// InboundMessage is the raw message from a channel webhook.
type InboundMessage struct {
	ChannelType string                 `json:"channel_type"`
	Sender      string                 `json:"sender"`
	Content     string                 `json:"content"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// InboundResult is returned by HandleInboundMessage.
type InboundResult struct {
	AgentUserID    *string `json:"agent_user_id,omitempty"`
	ConversationID *string `json:"conversation_id,omitempty"`
	MessageID      *string `json:"message_id,omitempty"`
	ExecutionID    *string `json:"execution_id,omitempty"`
}

// InboundPersistence defines the DB methods the inbound pipeline needs.
type InboundPersistence interface {
	persistence.IdentityResolver
	persistence.ConversationResolver
	persistence.MessageStore
	persistence.HistoryFetcher
	persistence.ExtractionDispatcher
	persistence.PendingActionChecker
	GetAgentByID(id string) (*api.Agent, error)
}

// FlowDispatcher triggers orchestrator flow executions.
type FlowDispatcher interface {
	DispatchFlow(flowID string, triggerID *string, data map[string]interface{}) error
}

// InboundHandler processes inbound agent messages with direct DB access.
type InboundHandler struct {
	persistence     InboundPersistence
	promptAssembler *SystemPromptAssembler
	dispatcher      FlowDispatcher
}

// NewInboundHandler creates a handler with the given dependencies.
func NewInboundHandler(p InboundPersistence, pa *SystemPromptAssembler, d FlowDispatcher) *InboundHandler {
	return &InboundHandler{
		persistence:     p,
		promptAssembler: pa,
		dispatcher:      d,
	}
}

// HandleInboundMessage runs the full 7-step pipeline with direct DB access.
// This replaces the Launch-side pipeline that made 7+ HTTP round-trips.
func (h *InboundHandler) HandleInboundMessage(agentID string, msg InboundMessage) (*InboundResult, error) {
	result := &InboundResult{}

	agent, err := h.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}

	// Step 1: resolve identity.
	var agentUserID *string
	externalID, displayName := DeriveExternalID(msg)
	if externalID != "" {
		identity, user, err := h.persistence.ResolveOrCreateAgentIdentity(
			agentID, agent.OrganisationID, msg.ChannelType, externalID, nil, &displayName)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id": agentID,
				"error":    err,
			}).Warn("failed to resolve identity, continuing without user scoping")
		} else if identity != nil && user != nil {
			agentUserID = &user.ID
			result.AgentUserID = agentUserID
		}
	}

	// Step 2: resolve conversation.
	var conversationID *string
	channelID, threadID := DeriveChannelScope(msg)
	if channelID != "" {
		// Use the agent's configured idle timeout, falling back to 30 minutes.
		idleTimeout := 1800
		if agent.IdleTimeoutSeconds > 0 {
			idleTimeout = agent.IdleTimeoutSeconds
		}
		conv, err := h.persistence.ResolveOrCreateAgentConversation(
			agentID, agentUserID, msg.ChannelType, channelID, threadID, idleTimeout)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id": agentID,
				"error":    err,
			}).Warn("failed to resolve conversation")
		} else if conv != nil && conv.Conversation != nil {
			id := conv.Conversation.ID
			conversationID = &id
			result.ConversationID = conversationID

			// Generate session summary for closed stale conversations.
			if conv.ClosedConversationID != nil {
				go h.generateSessionSummary(agentID, *conv.ClosedConversationID, agentUserID)
			}
		}
	}

	// Step 3: fetch conversation history BEFORE storing current turn.
	var conversationHistory []map[string]interface{}
	if conversationID != nil {
		msgs, err := h.persistence.GetAgentConversationMessages(*conversationID, defaultHistoryLimit)
		if err == nil {
			conversationHistory = normaliseMessages(msgs)
		}
	}

	// Step 4: store the inbound message.
	inboundMsg := api.AgentMessage{
		AgentID:        agentID,
		ConversationID: conversationID,
		Direction:      "inbound",
		ChannelType:    msg.ChannelType,
		Sender:         &msg.Sender,
		Content:        msg.Content,
	}
	if msg.Metadata != nil {
		metaJSON, _ := json.Marshal(msg.Metadata)
		inboundMsg.Metadata = json.RawMessage(metaJSON)
	}
	msgID, err := h.persistence.CreateAgentMessageInConversation(inboundMsg)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Error("failed to store inbound message")
	}
	result.MessageID = msgID

	// Step 5: dispatch orchestrator flow.
	if agent.OrchestratorFlowID != nil && *agent.OrchestratorFlowID != "" {
		triggerData := h.buildTriggerData(agent, msg, msgID, agentUserID, conversationID, conversationHistory)
		if err := h.dispatcher.DispatchFlow(*agent.OrchestratorFlowID, nil, triggerData); err != nil {
			log.WithFields(log.Fields{
				"agent_id": agentID,
				"error":    err,
			}).Error("failed to dispatch execution")
			return result, err
		}
	}

	// Step 6: dispatch extraction (fire-and-forget).
	go h.dispatchExtraction(agentID, msg, msgID, agentUserID, conversationID)

	// Step 7: check pending action confirmations.
	if agentUserID != nil && *agentUserID != "" {
		go h.checkPendingActionConfirmation(agentID, msg, *agentUserID)
	}

	return result, nil
}

func (h *InboundHandler) buildTriggerData(
	agent *api.Agent,
	msg InboundMessage,
	msgID *string,
	agentUserID *string,
	conversationID *string,
	history []map[string]interface{},
) map[string]interface{} {
	data := map[string]interface{}{
		"agent_id":       agent.ID,
		"channel_type":   msg.ChannelType,
		"sender":         msg.Sender,
		"content":        msg.Content,
		"trigger_source": "channel",
	}

	// Assemble system prompt via the API-side assembler (direct DB).
	if h.promptAssembler != nil {
		userID := ""
		if agentUserID != nil {
			userID = *agentUserID
		}
		persona := ""
		if agent.SystemPrompt != nil {
			persona = *agent.SystemPrompt
		}
		result := h.promptAssembler.AssembleSystemPrompt(SystemPromptRequest{
			AgentID:     agent.ID,
			Persona:     persona,
			ChannelType: msg.ChannelType,
			AgentUserID: userID,
			Content:     msg.Content,
		})
		if result.Prompt != "" {
			data["system_prompt"] = result.Prompt
		}
	}

	if msgID != nil {
		data["message_id"] = *msgID
	}
	if agentUserID != nil {
		data["agent_user_id"] = *agentUserID
	}
	if conversationID != nil {
		data["conversation_id"] = *conversationID
	}
	if history != nil {
		data["conversation_history"] = history
	}

	// Flatten metadata into trigger data.
	for k, v := range msg.Metadata {
		if _, exists := data[k]; !exists {
			data[k] = v
		}
	}

	return data
}

func (h *InboundHandler) dispatchExtraction(
	agentID string,
	msg InboundMessage,
	msgID *string,
	agentUserID *string,
	conversationID *string,
) {
	// Build enriched content for short messages.
	enrichedContent := msg.Content
	if conversationID != nil && len(msg.Content) < 80 {
		msgs, err := h.persistence.GetAgentConversationMessages(*conversationID, 4)
		if err == nil && len(msgs) > 0 {
			var sb strings.Builder
			sb.WriteString("Recent conversation:\n")
			for _, m := range msgs {
				role := "user"
				if m.Direction == "outbound" {
					role = "assistant"
				}
				fmt.Fprintf(&sb, "[%s]: %s\n", role, m.Content)
			}
			sb.WriteString("\nCurrent message: ")
			sb.WriteString(msg.Content)
			enrichedContent = sb.String()
		}
	}

	h.persistence.DispatchExtraction(agentID, enrichedContent, "user", msgID, agentUserID, conversationID)
}

func (h *InboundHandler) generateSessionSummary(agentID, closedConvID string, agentUserID *string) {
	msgs, err := h.persistence.GetAgentConversationMessages(closedConvID, 50)
	if err != nil || len(msgs) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("Summarise this completed conversation in 2-3 sentences. ")
	sb.WriteString("Focus on: what the user asked for, what was accomplished, ")
	sb.WriteString("and any outstanding items. Write as a factual summary, not as a message.\n\n")
	for _, m := range msgs {
		role := "user"
		if m.Direction == "outbound" {
			role = "assistant"
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", role, m.Content)
	}

	userID := ""
	if agentUserID != nil {
		userID = *agentUserID
	}
	h.persistence.DispatchExtraction(agentID, sb.String(), "summary", nil, &userID, &closedConvID)
}

func (h *InboundHandler) checkPendingActionConfirmation(agentID string, msg InboundMessage, agentUserID string) {
	content := msg.Content
	if msg.ChannelType == "email" {
		content = extractEmailBody(content)
	}

	normalised := strings.TrimSpace(strings.ToLower(content))
	if len(normalised) > 30 {
		return
	}

	isConfirm := affirmatives[normalised]
	isDecline := decliners[normalised]
	if !isConfirm && !isDecline {
		return
	}

	actions, err := h.persistence.GetOpenPendingActionsForUser(agentUserID)
	if err != nil || len(actions) == 0 {
		return
	}

	for _, pa := range actions {
		if pa.NotifiedAt == nil || pa.Status != "awaiting_confirmation" {
			continue
		}

		newStatus := "declined"
		if isConfirm {
			newStatus = "confirmed_here_awaiting_other_side"
		}

		if err := h.persistence.UpdatePendingActionStatus(pa.ID, newStatus); err != nil {
			continue
		}

		log.WithFields(log.Fields{
			"agent_id":          agentID,
			"pending_action_id": pa.ID,
			"type":              pa.Type,
			"resolution":        newStatus,
		}).Info("pending action resolved via short reply detection")

		if isConfirm {
			switch pa.Type {
			case "identity_link":
				go h.persistence.RequestCrossChannelVerification(agentID, pa.ID, agentUserID)
			case "identity_link_verification":
				go h.persistence.TriggerIdentityMerge(agentID, pa.ID)
			}
		}
		break
	}
}

// normaliseMessages converts agent_message rows to role/content maps.
// Tool exchange messages (tool_use/tool_result) are excluded — they are
// internal mechanics within a single AI turn. The final outbound message
// already summarises the tool results. Including them confuses the model
// into thinking the user said the tool results.
func normaliseMessages(msgs []*api.AgentMessage) []map[string]interface{} {
	var result []map[string]interface{}
	for _, msg := range msgs {
		switch msg.Direction {
		case "inbound":
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		case "outbound":
			result = append(result, map[string]interface{}{
				"role":    "assistant",
				"content": msg.Content,
			})
		case "tool_use", "tool_result":
			// Skip — internal to a single AI turn.
			continue
		default:
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		}
	}
	return result
}

// DeriveExternalID extracts the stable channel-specific ID from message metadata.
func DeriveExternalID(msg InboundMessage) (externalID, displayName string) {
	displayName = msg.Sender
	if msg.Metadata == nil {
		return msg.Sender, displayName
	}
	switch msg.ChannelType {
	case "slack":
		if v, ok := msg.Metadata["user_id"].(string); ok && v != "" {
			externalID = v
		}
		if v, ok := msg.Metadata["user_name"].(string); ok && v != "" {
			displayName = v
		} else if v, ok := msg.Metadata["display_name"].(string); ok && v != "" {
			displayName = v
		}
	case "telegram":
		if v, ok := msg.Metadata["sender_id"].(string); ok && v != "" {
			externalID = v
		}
		if v, ok := msg.Metadata["sender_name"].(string); ok && v != "" {
			displayName = v
		} else if v, ok := msg.Metadata["sender_username"].(string); ok && v != "" {
			displayName = "@" + v
		}
	case "email":
		if v, ok := msg.Metadata["from"].(string); ok && v != "" {
			externalID = extractBareEmail(v)
			displayName = v
		}
	}
	if externalID == "" {
		externalID = msg.Sender
	}
	return externalID, displayName
}

// DeriveChannelScope extracts the channel ID and optional thread ID.
func DeriveChannelScope(msg InboundMessage) (channelID string, threadID *string) {
	if msg.Metadata == nil {
		return "", nil
	}
	switch msg.ChannelType {
	case "slack":
		if v, ok := msg.Metadata["channel_id"].(string); ok {
			channelID = v
		}
		if v, ok := msg.Metadata["thread_ts"].(string); ok && v != "" {
			t := v
			threadID = &t
		}
	case "telegram":
		if v, ok := msg.Metadata["chat_id"].(string); ok {
			channelID = v
		}
	case "email":
		if v, ok := msg.Metadata["account"].(string); ok {
			channelID = v
		}
		if v, ok := msg.Metadata["thread_id"].(string); ok && v != "" {
			t := v
			threadID = &t
		}
	}
	return channelID, threadID
}

var affirmatives = map[string]bool{
	"yes": true, "yes please": true, "yep": true, "yeah": true,
	"sure": true, "go ahead": true, "confirm": true, "confirmed": true,
	"do it": true, "link them": true, "link it": true,
	"ok": true, "okay": true, "y": true,
}

var decliners = map[string]bool{
	"no": true, "nope": true, "nah": true, "don't": true,
	"cancel": true, "decline": true, "declined": true, "n": true,
}

// extractBareEmail strips the display name from an email address.
func extractBareEmail(addr string) string {
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.LastIndex(addr, ">"); j > i {
			return strings.TrimSpace(addr[i+1 : j])
		}
	}
	return strings.TrimSpace(addr)
}

// extractEmailBody strips headers and quoted replies from email content.
func extractEmailBody(content string) string {
	lines := strings.Split(content, "\n")
	var body []string
	inBody := false
	for _, line := range lines {
		if !inBody {
			if strings.TrimSpace(line) == "" {
				inBody = true
			}
			continue
		}
		if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "On ") && strings.Contains(line, "wrote:") {
			break
		}
		body = append(body, line)
	}
	if len(body) == 0 {
		return content
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}
