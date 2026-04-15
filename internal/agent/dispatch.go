package agent

import (
	"encoding/json"
	"fmt"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// ExecutionNotifier wakes long-polling runners.
type ExecutionNotifier interface {
	Notify(tags ...string)
}

// DispatchPersistence defines the DB methods needed for flow dispatch.
type DispatchPersistence interface {
	TriggerExecution(floID, triggerID string, data interface{}) (*string, error)
	GetTriggersByFloID(floID string) ([]*api.Trigger, error)
	SetExecutionAgentID(executionID, agentID string) error
	GetActiveAgentSession(agentID string) (*api.AgentSession, error)
	SetExecutionAgentSessionID(executionID, sessionID string) error
	GetAgentByID(id string) (*api.Agent, error)
}

// DirectFlowDispatcher dispatches flows via direct persistence calls
// instead of HTTP self-calls. This is the Phase 4 replacement for
// HTTPFlowDispatcher.
type DirectFlowDispatcher struct {
	persistence DispatchPersistence
	notifier    ExecutionNotifier
}

// NewDirectFlowDispatcher creates a dispatcher with direct DB access.
func NewDirectFlowDispatcher(p DispatchPersistence, n ExecutionNotifier) *DirectFlowDispatcher {
	return &DirectFlowDispatcher{persistence: p, notifier: n}
}

// DispatchFlow triggers a flow execution via direct persistence calls.
func (d *DirectFlowDispatcher) DispatchFlow(flowID string, triggerID *string, data map[string]interface{}) error {
	var tid string

	if triggerID != nil && *triggerID != "" {
		tid = *triggerID
	} else {
		// Resolve trigger: match by channel_type first, then manual, then any.
		triggers, err := d.persistence.GetTriggersByFloID(flowID)
		if err != nil {
			return fmt.Errorf("failed to get triggers: %w", err)
		}

		channelType, _ := data["channel_type"].(string)
		if channelType != "" {
			for _, t := range triggers {
				if t.TypeName == channelType {
					tid = t.ID
					break
				}
			}
		}
		if tid == "" {
			for _, t := range triggers {
				if t.TypeName == "manual" {
					tid = t.ID
					break
				}
			}
		}
		if tid == "" && len(triggers) > 0 {
			tid = triggers[0].ID
		}
		if tid == "" {
			return fmt.Errorf("flow %s has no trigger", flowID)
		}
	}

	executionID, err := d.persistence.TriggerExecution(flowID, tid, data)
	if err != nil {
		return fmt.Errorf("trigger execution failed: %w", err)
	}

	// Tag agent-dispatched executions.
	if executionID != nil && data != nil {
		if agentID, ok := data["agent_id"].(string); ok && agentID != "" {
			if err := d.persistence.SetExecutionAgentID(*executionID, agentID); err != nil {
				log.WithFields(log.Fields{
					"error":        err,
					"execution_id": *executionID,
					"agent_id":     agentID,
				}).Warn("unable to set execution agent id")
			}
			if session, _ := d.persistence.GetActiveAgentSession(agentID); session != nil {
				_ = d.persistence.SetExecutionAgentSessionID(*executionID, session.ID)
			}
		}
	}

	d.notifier.Notify()
	return nil
}

// DispatchExtraction triggers the extraction pipeline for an agent.
// Direct replacement for the extractAgentInternal HTTP handler.
func DispatchExtraction(
	p DispatchPersistence,
	notifier ExecutionNotifier,
	agentID, content, role string,
	msgID, agentUserID, conversationID *string,
) {
	agent, err := p.GetAgentByID(agentID)
	if err != nil || agent == nil {
		return
	}
	if agent.ExtractionFlowID == nil || *agent.ExtractionFlowID == "" {
		return // No extraction flow configured — silent no-op.
	}

	triggers, err := p.GetTriggersByFloID(*agent.ExtractionFlowID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to resolve extraction flow triggers")
		return
	}

	var triggerID string
	for _, t := range triggers {
		if t.TypeName == "manual" {
			triggerID = t.ID
			break
		}
	}
	if triggerID == "" && len(triggers) > 0 {
		triggerID = triggers[0].ID
	}
	if triggerID == "" {
		log.WithField("agent_id", agentID).Error("extraction flow has no usable trigger")
		return
	}

	triggerData := map[string]interface{}{
		"agent_id": agentID,
		"role":     role,
		"content":  content,
	}
	if msgID != nil {
		triggerData["message_id"] = *msgID
	}
	if agentUserID != nil {
		triggerData["agent_user_id"] = *agentUserID
	}
	if conversationID != nil {
		triggerData["conversation_id"] = *conversationID
	}
	if agent.AIAPIKey != nil && *agent.AIAPIKey != "" {
		triggerData["api_key"] = *agent.AIAPIKey
	}

	raw, err := json.Marshal(triggerData)
	if err != nil {
		return
	}

	executionID, err := p.TriggerExecution(*agent.ExtractionFlowID, triggerID, json.RawMessage(raw))
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to trigger extraction execution")
		return
	}

	if executionID != nil {
		_ = p.SetExecutionAgentID(*executionID, agentID)
	}

	notifier.Notify()
}
