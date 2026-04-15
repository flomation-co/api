package poller

import (
	"fmt"
	"time"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// CommitmentPersistence defines the DB methods the commitment poller needs.
type CommitmentPersistence interface {
	GetDueCommitments(limit int) ([]*api.AgentCommitment, error)
	UpdateCommitmentStatus(id, status string) error
	GetAgentByID(id string) (*api.Agent, error)
	GetAgentConversationByID(id string) (*api.AgentConversation, error)
	GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error)
}

// FlowDispatcher dispatches flow executions. Decouples the poller from
// the HTTP layer so it can trigger flows via the internal execution path.
type FlowDispatcher interface {
	DispatchFlow(flowID string, triggerID *string, data map[string]interface{}) error
}

// CommitmentPoller fires due commitments by dispatching orchestrator
// flows. Runs every 30 seconds.
type CommitmentPoller struct {
	persistence CommitmentPersistence
	dispatcher  FlowDispatcher
}

// StartCommitmentPoller creates and starts the commitment poller goroutine.
func StartCommitmentPoller(p CommitmentPersistence, d FlowDispatcher) *CommitmentPoller {
	cp := &CommitmentPoller{persistence: p, dispatcher: d}
	go cp.watch()
	return cp
}

func (cp *CommitmentPoller) watch() {
	time.Sleep(10 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info("commitment poller started (API-side, 30s interval)")

	for range ticker.C {
		cp.poll()
	}
}

func (cp *CommitmentPoller) poll() {
	commitments, err := cp.persistence.GetDueCommitments(50)
	if err != nil {
		log.WithError(err).Warn("commitment poller: failed to fetch due commitments")
		return
	}
	if len(commitments) == 0 {
		return
	}

	log.WithField("count", len(commitments)).Debug("processing due commitments")

	for _, c := range commitments {
		cp.processCommitment(c)
	}
}

func (cp *CommitmentPoller) processCommitment(c *api.AgentCommitment) {
	l := log.WithFields(log.Fields{
		"commitment_id": c.ID,
		"agent_id":      c.AgentID,
		"kind":          c.Kind,
	})

	// Claim by transitioning to 'firing'.
	if err := cp.persistence.UpdateCommitmentStatus(c.ID, "firing"); err != nil {
		l.WithError(err).Warn("failed to claim commitment")
		return
	}

	// Look up agent for orchestrator flow ID.
	agent, err := cp.persistence.GetAgentByID(c.AgentID)
	if err != nil || agent == nil {
		l.Warn("agent not found, expiring commitment")
		_ = cp.persistence.UpdateCommitmentStatus(c.ID, "expired")
		return
	}
	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		l.Warn("agent has no orchestrator flow, expiring commitment")
		_ = cp.persistence.UpdateCommitmentStatus(c.ID, "expired")
		return
	}

	content := fmt.Sprintf("[SCHEDULED REMINDER] You previously promised to: %s. "+
		"Send a brief, friendly reminder to the user about this. "+
		"Do not discuss the conversation history in detail — just deliver the reminder naturally.",
		c.Description)

	triggerData := map[string]interface{}{
		"agent_id":       c.AgentID,
		"trigger_source": "commitment",
		"commitment_id":  c.ID,
		"content":        content,
		"sender":         "system",
	}
	if c.AgentUserID != nil {
		triggerData["agent_user_id"] = *c.AgentUserID
	}
	if c.ConversationID != nil {
		triggerData["conversation_id"] = *c.ConversationID

		// Look up conversation for channel routing.
		if conv, err := cp.persistence.GetAgentConversationByID(*c.ConversationID); err == nil && conv != nil {
			triggerData["channel_type"] = conv.ChannelType
			triggerData["channel_id"] = conv.ChannelID
			if conv.ThreadID != nil && *conv.ThreadID != "" {
				triggerData["thread_id"] = *conv.ThreadID
			}
		}

		// Include recent conversation history.
		if msgs, err := cp.persistence.GetAgentConversationMessages(*c.ConversationID, 5); err == nil {
			history := normaliseHistory(msgs)
			if len(history) > 0 {
				triggerData["conversation_history"] = history
			}
		}
	}

	if _, has := triggerData["channel_type"]; !has {
		triggerData["channel_type"] = "commitment"
	}

	if len(c.Payload) > 0 && string(c.Payload) != "{}" && string(c.Payload) != "null" {
		triggerData["commitment_payload"] = c.Payload
	}

	// Build system prompt.
	systemPrompt := ""
	if agent.SystemPrompt != nil {
		systemPrompt = *agent.SystemPrompt
	}
	systemPrompt += "\n\n━━━ Platform capabilities ━━━\n" +
		"You CAN set reminders and follow up proactively. The platform " +
		"delivers scheduled reminders automatically. When this execution " +
		"was triggered, it was because YOU previously promised to remind " +
		"the user. Deliver the reminder naturally as yourself — do not " +
		"break character, do not disclaim your abilities, do not mention " +
		"being an AI. You are fulfilling a promise you made.\n\n" +
		"━━━ Current time ━━━\n" + time.Now().Format("Monday, 2 January 2006 15:04 MST")
	triggerData["system_prompt"] = systemPrompt

	// Dispatch.
	if err := cp.dispatcher.DispatchFlow(*agent.OrchestratorFlowID, nil, triggerData); err != nil {
		l.WithError(err).Warn("failed to dispatch commitment, rolling back to pending")
		_ = cp.persistence.UpdateCommitmentStatus(c.ID, "pending")
		return
	}

	if err := cp.persistence.UpdateCommitmentStatus(c.ID, "fulfilled"); err != nil {
		l.WithError(err).Warn("failed to mark commitment fulfilled (dispatch already sent)")
	}

	l.Info("commitment fired and fulfilled")
}

// normaliseHistory converts agent_message rows to role/content maps
// for the AI action's conversation_history input.
// Tool exchange messages are excluded — they confuse the model.
func normaliseHistory(msgs []*api.AgentMessage) []map[string]interface{} {
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
			continue // internal to a single AI turn
		default:
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		}
	}
	return result
}
