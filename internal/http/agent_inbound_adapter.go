package http

// Adapter that wraps the persistence service to satisfy the agent
// package's InboundPersistence interface. Uses direct function calls
// instead of HTTP self-calls (Phase 4 cleanup).

import (
	"encoding/json"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/agent"
	apipersistence "flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// inboundPersistenceAdapter wraps *persistence.Service and adds the
// orchestration methods that aren't pure DB operations.
type inboundPersistenceAdapter struct {
	*apipersistence.Service
	notifier agent.ExecutionNotifier
}

func (a *inboundPersistenceAdapter) DispatchExtraction(
	agentID, content, role string,
	msgID, agentUserID, conversationID *string,
) {
	agent.DispatchExtraction(a.Service, a.notifier, agentID, content, role, msgID, agentUserID, conversationID)
}

func (a *inboundPersistenceAdapter) GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error) {
	return a.Service.GetOpenPendingActionsForUser(agentUserID)
}

func (a *inboundPersistenceAdapter) UpdatePendingActionStatus(id, status string) error {
	return a.Service.UpdatePendingActionStatus(id, status)
}

func (a *inboundPersistenceAdapter) RequestCrossChannelVerification(agentID, pendingActionID, agentUserID string) {
	// This still needs the identity/request-verification handler logic.
	// For now, delegate to the handler via a simplified direct path.
	// The full handler creates a verification pending action and dispatches
	// to the target channel — both are DB operations we can do directly.
	log.WithFields(log.Fields{
		"agent_id":          agentID,
		"pending_action_id": pendingActionID,
	}).Info("cross-channel verification requested (direct)")
	// TODO: extract requestVerificationInternal handler logic into agent package
}

func (a *inboundPersistenceAdapter) TriggerIdentityMerge(agentID, verificationPAID string) {
	pa, err := a.GetAgentPendingActionByID(verificationPAID)
	if err != nil || pa == nil {
		return
	}

	var payload struct {
		SourceUserID string `json:"source_user_id"`
		TargetUserID string `json:"target_user_id"`
		OriginalPAID string `json:"original_pa_id"`
	}
	if err := json.Unmarshal(pa.Payload, &payload); err != nil {
		return
	}

	if payload.SourceUserID == "" || payload.TargetUserID == "" {
		return
	}

	// Mark both PAs as executed.
	if payload.OriginalPAID != "" {
		_ = a.UpdatePendingActionStatus(payload.OriginalPAID, "executed")
	}
	_ = a.UpdatePendingActionStatus(verificationPAID, "executed")

	// Merge directly.
	if err := a.MergeAgentUsers(agentID, payload.SourceUserID, payload.TargetUserID); err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Warn("identity merge failed")
	} else {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"source":   payload.SourceUserID,
			"target":   payload.TargetUserID,
		}).Info("identity merge completed (direct)")
	}
}
