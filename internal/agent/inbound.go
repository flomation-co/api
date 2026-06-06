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
	PlatformUserID *string `json:"user_id,omitempty"`
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

	// User-declared identity resolution (R2). When the agent runs in an
	// organisation, the inbound pipeline resolves the platform user_id
	// either via a declared user_identity or by upserting an anonymous
	// stub user keyed per-(org, channel, external_id). After resolution
	// the full declared-identity set for the resolved user is snapshot
	// onto triggerData so flows can read ${flow.identities}.
	LookupUserIdentityByChannel(organisationID *string, channelType, externalID string) (*api.UserIdentity, error)
	UpsertAnonymousUser(organisationID, channelType, externalID, displayName string) (string, error)
	GetUserIdentitiesByUserAndOrg(userID string, organisationID *string) ([]*api.UserIdentity, error)
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

	if agent.Status == api.AgentStatusPaused {
		log.WithField("agent_id", agentID).Info("ignoring inbound message — agent is paused")
		return result, nil
	}

	// Step 1: resolve identity.
	// Normalise channel sub-types to their base type for identity resolution.
	// telegram_voice is the same user as telegram — they shouldn't get
	// separate identities just because one message was a voice note.
	identityChannelType := normaliseChannelType(msg.ChannelType)

	var agentUserID *string
	externalID, displayName := DeriveExternalID(msg)
	if externalID != "" {
		identity, user, err := h.persistence.ResolveOrCreateAgentIdentity(
			agentID, agent.OrganisationID, identityChannelType, externalID, nil, &displayName)
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

	// Step 1.5 (R2 + R3 Phase 1.5): resolve the platform user_id
	// alongside the agent_user_id. The agent_user_id remains the
	// canonical memory scope for now; the platform user_id is exposed
	// to flows via ${flow.user_id} so declared identities become
	// visible to orchestrator logic. Runs for both org-scoped AND
	// personal-mode agents — the lookup query uses COALESCE so NULL
	// organisation_id (personal mode) matches NULL declarations.
	//
	// Anonymous-user upsert only runs in org-scoped mode: the users
	// table CHECK constraint requires organisation_id when
	// is_anonymous=true (personal-mode unknown senders have no
	// platform user_id and fall back to agent_user-only scoping).
	var platformUserID *string
	if externalID != "" {
		declared, err := h.persistence.LookupUserIdentityByChannel(agent.OrganisationID, identityChannelType, externalID)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id":     agentID,
				"channel_type": identityChannelType,
				"error":        err,
			}).Warn("user_identity lookup failed; treating as unrecognised sender")
		}
		switch {
		case declared != nil:
			id := declared.UserID
			platformUserID = &id
		case agent.OrganisationID != nil && *agent.OrganisationID != "":
			// Org-scoped + unrecognised → anonymous stub user.
			orgID := *agent.OrganisationID
			anonID, upsertErr := h.persistence.UpsertAnonymousUser(orgID, identityChannelType, externalID, displayName)
			if upsertErr != nil {
				log.WithFields(log.Fields{
					"agent_id":     agentID,
					"channel_type": identityChannelType,
					"error":        upsertErr,
				}).Warn("anonymous user upsert failed; continuing without platform user scoping")
			} else if anonID != "" {
				platformUserID = &anonID
			}
		}
		if platformUserID != nil {
			result.PlatformUserID = platformUserID
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
			agentID, agentUserID, identityChannelType, channelID, threadID, idleTimeout)
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
		// Snapshot the user's declared identities for ${flow.identities}.
		// Scope matches the agent: org-scoped agents see the user's
		// org-scoped declarations; personal agents see the user's
		// personal-mode (NULL org) declarations. Empty for anonymous
		// users (no rows in user_identity) — correct read of "this
		// person has nothing declared".
		var identities []*api.UserIdentity
		if platformUserID != nil {
			ids, idErr := h.persistence.GetUserIdentitiesByUserAndOrg(*platformUserID, agent.OrganisationID)
			if idErr != nil {
				log.WithFields(log.Fields{
					"agent_id":         agentID,
					"platform_user_id": *platformUserID,
					"error":            idErr,
				}).Warn("failed to fetch user identities, continuing with empty set")
			} else {
				identities = ids
			}
		}
		triggerData := h.buildTriggerData(agent, msg, msgID, agentUserID, platformUserID, identities, conversationID, conversationHistory)
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
	platformUserID *string,
	identities []*api.UserIdentity,
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
	if platformUserID != nil {
		data["user_id"] = *platformUserID
		if agent.OrganisationID != nil {
			data["organisation_id"] = *agent.OrganisationID
		}
	}
	if len(identities) > 0 {
		// Shape the slice to match the executor's ExecutionIdentity struct
		// so flow.Get("identities") can unmarshal directly. Always non-nil
		// when populated — empty stays absent from triggerData.
		out := make([]map[string]interface{}, 0, len(identities))
		for _, i := range identities {
			if i == nil {
				continue
			}
			row := map[string]interface{}{
				"channel_type": i.ChannelType,
				"external_id":  i.ExternalID,
			}
			if i.DisplayName != nil && *i.DisplayName != "" {
				row["display_name"] = *i.DisplayName
			}
			out = append(out, row)
		}
		data["identities"] = out
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
			switch pa.Type {
			case "identity_link":
				newStatus = "confirmed_here_awaiting_other_side"
			case "identity_link_verification":
				newStatus = "confirmed_here_awaiting_other_side"
			default:
				// For non-identity actions (forget_memory, correct_memory, etc.)
				// a confirmation resolves them immediately — no second side needed.
				newStatus = "resolved"
			}
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

// normaliseChannelType maps channel sub-types to their base type for identity
// and conversation resolution. Voice, video, and other media variants of a
// channel are the same user on the same platform.
func normaliseChannelType(channelType string) string {
	switch channelType {
	case "telegram_voice":
		return "telegram"
	case "twilio_voice":
		return "twilio"
	default:
		return channelType
	}
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
	case "telegram", "telegram_voice":
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
	case "twilio_sms", "twilio_voice":
		if v, ok := msg.Metadata["user_id"].(string); ok && v != "" {
			externalID = v
		}
		displayName = externalID
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
	case "telegram", "telegram_voice":
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
	case "twilio_sms":
		if v, ok := msg.Metadata["user_id"].(string); ok {
			channelID = v
		}
	case "twilio_voice":
		if v, ok := msg.Metadata["user_id"].(string); ok {
			channelID = v
		}
		if v, ok := msg.Metadata["call_sid"].(string); ok && v != "" {
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
