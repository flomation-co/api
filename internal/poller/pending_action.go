package poller

import (
	"encoding/json"
	"fmt"
	"time"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// PendingActionPersistence defines the DB methods the pending action poller needs.
type PendingActionPersistence interface {
	GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error)
	MarkPendingActionNotified(id string) error
	GetAgentByID(id string) (*api.Agent, error)
	GetAgentConversationByID(id string) (*api.AgentConversation, error)
	GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error)
	GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error)
}

// pollerActionTypes is the set of types the poller dispatches for.
var pollerActionTypes = map[string]bool{
	"identity_link":              true,
	"identity_link_verification": true,
}

// PendingActionPoller dispatches proactive confirmation prompts for
// unnotified pending actions. Runs every 15 seconds.
type PendingActionPoller struct {
	persistence PendingActionPersistence
	dispatcher  FlowDispatcher
}

// StartPendingActionPoller creates and starts the pending action poller goroutine.
func StartPendingActionPoller(p PendingActionPersistence, d FlowDispatcher) *PendingActionPoller {
	pap := &PendingActionPoller{persistence: p, dispatcher: d}
	go pap.watch()
	return pap
}

func (pap *PendingActionPoller) watch() {
	time.Sleep(12 * time.Second)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Info("pending action poller started (API-side, 15s interval)")

	for range ticker.C {
		pap.poll()
	}
}

func (pap *PendingActionPoller) poll() {
	actions, err := pap.persistence.GetUnnotifiedPendingActions(20)
	if err != nil {
		log.WithError(err).Warn("pending action poller: failed to fetch unnotified")
		return
	}
	if len(actions) == 0 {
		return
	}

	log.WithField("count", len(actions)).Debug("processing unnotified pending actions")

	for _, pa := range actions {
		pap.processAction(pa)
	}
}

func (pap *PendingActionPoller) processAction(pa *api.AgentPendingAction) {
	l := log.WithFields(log.Fields{
		"pending_action_id": pa.ID,
		"agent_id":          pa.AgentID,
		"type":              pa.Type,
	})

	// Only dispatch user-facing messages for identity linking types.
	if !pollerActionTypes[pa.Type] {
		_ = pap.persistence.MarkPendingActionNotified(pa.ID)
		l.Debug("skipping non-identity pending action type")
		return
	}

	agent, err := pap.persistence.GetAgentByID(pa.AgentID)
	if err != nil || agent == nil {
		l.Warn("agent not found, skipping")
		return
	}
	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		l.Warn("agent has no orchestrator flow, skipping")
		return
	}

	// Build the confirmation prompt content.
	var content string
	switch pa.Type {
	case "identity_link":
		content = fmt.Sprintf(
			"[SYSTEM: IDENTITY LINK REQUEST] The user previously said: %q. "+
				"They mentioned an identity on another messaging channel. "+
				"Ask them to CONFIRM that this is them so you can link their conversations. "+
				"This is NOT about connecting Google accounts or OAuth — this is about "+
				"recognising them as the same person when they message you from different "+
				"channels (e.g. Telegram vs Email). Say something like: "+
				"\"You mentioned you're also reachable at [identity] — shall I link that "+
				"so I recognise you as the same person across channels? Just say yes to confirm.\"",
			pa.Evidence)
	case "identity_link_verification":
		var payload map[string]interface{}
		_ = json.Unmarshal(pa.Payload, &payload)
		sourceChannel, _ := payload["source_channel"].(string)
		if sourceChannel == "" {
			sourceChannel = "another channel"
		}
		content = fmt.Sprintf(
			"[SYSTEM: IDENTITY VERIFICATION — SEND A NEW MESSAGE] "+
				"You need to verify this user's identity. They were linked from %s. "+
				"Send them a natural, friendly message — NOT a robotic verification. "+
				"For email: use email_send (NOT email_reply). "+
				"Write something like: \"Hey, just checking — is this the right email for you? "+
				"Someone on %s mentioned this address. Just reply yes if that's you.\" "+
				"Keep your own voice and personality. Don't mention 'identity verification' "+
				"or 'claims to be you' — just confirm it's them naturally.",
			sourceChannel, sourceChannel)
	default:
		content = fmt.Sprintf(
			"[SYSTEM: PENDING CONFIRMATION] A %s needs confirmation. Context: %q. "+
				"Ask the user to confirm or decline.",
			pa.Type, pa.Evidence)
	}

	triggerData := map[string]interface{}{
		"agent_id":          pa.AgentID,
		"agent_user_id":     pa.AgentUserID,
		"trigger_source":    "pending_action",
		"pending_action_id": pa.ID,
		"content":           content,
		"sender":            "system",
	}

	// Route to the appropriate channel.
	if pa.Type == "identity_link_verification" {
		identities, err := pap.persistence.GetAgentIdentitiesByUserID(pa.AgentUserID)
		if err == nil && len(identities) > 0 {
			identity := identities[0]
			triggerData["channel_type"] = identity.ChannelType
			triggerData["channel_external_id"] = identity.ChannelExternalID
			triggerData["recipient"] = identity.ChannelExternalID

			switch identity.ChannelType {
			case "email":
				triggerData["from"] = identity.ChannelExternalID
				content = fmt.Sprintf(
					"[SYSTEM: SEND VERIFICATION EMAIL to %s] "+
						"Use email_send (NOT email_reply). Recipient: %s. "+
						"Subject: something casual like \"Quick check\" or \"Is this you?\". "+
						"Body: Write a natural, friendly message checking this is the right email. "+
						"Something like: \"Hey, just wanted to check this is the right email for you — "+
						"I've got you on another channel and want to make sure I recognise you across both. "+
						"Just reply yes if that's you!\" Keep your personality. Don't be robotic.",
					identity.ChannelExternalID, identity.ChannelExternalID)
				triggerData["content"] = content
			case "telegram":
				triggerData["sender_id"] = identity.ChannelExternalID
			case "slack":
				triggerData["user_id"] = identity.ChannelExternalID
			}
		}
	} else {
		if pa.SourceConversation != nil && *pa.SourceConversation != "" {
			triggerData["conversation_id"] = *pa.SourceConversation

			if conv, err := pap.persistence.GetAgentConversationByID(*pa.SourceConversation); err == nil && conv != nil {
				triggerData["channel_type"] = conv.ChannelType
				triggerData["channel_id"] = conv.ChannelID
				if conv.ThreadID != nil && *conv.ThreadID != "" {
					triggerData["thread_id"] = *conv.ThreadID
				}
			}
		}
	}

	if _, has := triggerData["channel_type"]; !has {
		triggerData["channel_type"] = "system"
	}

	// Build system prompt.
	systemPrompt := ""
	if agent.SystemPrompt != nil {
		systemPrompt = *agent.SystemPrompt
	}
	if pa.Type == "identity_link_verification" {
		systemPrompt += "\n\n━━━ URGENT TASK ━━━\n" +
			"You are sending a verification message to a user on a different channel. " +
			"Someone on another messaging platform claims to also be this user. " +
			"Your ONLY job right now is to send them a message asking to confirm. " +
			"For EMAIL: use the email_send tool (NOT email_reply). The recipient address is in the message content. " +
			"For TELEGRAM: send a regular message. For SLACK: send a regular message. " +
			"Do NOT ask for more context. Do NOT say you don't have records. " +
			"Just send the verification message as instructed in the content.\n"
	} else {
		systemPrompt += "\n\n━━━ ACTION REQUIRED ━━━\n" +
			"CRITICAL: You MUST address the pending confirmation described in the message. " +
			"This is about IDENTITY LINKING — recognising the user as the same person across " +
			"different messaging channels (Telegram, Slack, Email, etc). It is NOT about " +
			"Google OAuth, calendar connections, or account settings. " +
			"Ask the user to confirm with a simple yes/no. Do NOT offer OAuth links or connection options.\n"
	}
	triggerData["system_prompt"] = systemPrompt

	// Include conversation history for context.
	if pa.SourceConversation != nil && *pa.SourceConversation != "" {
		if msgs, err := pap.persistence.GetAgentConversationMessages(*pa.SourceConversation, 5); err == nil {
			history := normaliseHistory(msgs)
			if len(history) > 0 {
				triggerData["conversation_history"] = history
			}
		}
	}

	// Dispatch.
	if err := pap.dispatcher.DispatchFlow(*agent.OrchestratorFlowID, nil, triggerData); err != nil {
		l.WithError(err).Warn("failed to dispatch pending action confirmation")
		return
	}

	if err := pap.persistence.MarkPendingActionNotified(pa.ID); err != nil {
		l.WithError(err).Warn("failed to mark pending action as notified (dispatch already sent)")
	}

	l.Info("pending action confirmation dispatched")
}
