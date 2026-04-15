package http

// Adapter that wraps the persistence service to satisfy the agent
// package's InboundPersistence interface. Adds extraction dispatch and
// identity merge methods that call the existing HTTP handlers internally.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	api "flomation.app/automate/api"
	apipersistence "flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// inboundPersistenceAdapter wraps *persistence.Service and adds the
// orchestration methods that aren't pure DB operations.
type inboundPersistenceAdapter struct {
	*apipersistence.Service
	selfURL string
}

func (a *inboundPersistenceAdapter) DispatchExtraction(
	agentID, content, role string,
	msgID, agentUserID, conversationID *string,
) {
	body := map[string]interface{}{
		"role":    role,
		"content": content,
	}
	if msgID != nil {
		body["message_id"] = *msgID
	}
	if agentUserID != nil {
		body["agent_user_id"] = *agentUserID
	}
	if conversationID != nil {
		body["conversation_id"] = *conversationID
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return
	}

	endpoint := fmt.Sprintf("%s/api/v1/internal/agent/%s/extract", a.selfURL, agentID)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload)) // #nosec G107
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Warn("inbound adapter: failed to dispatch extraction")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (a *inboundPersistenceAdapter) GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error) {
	return a.Service.GetOpenPendingActionsForUser(agentUserID)
}

func (a *inboundPersistenceAdapter) UpdatePendingActionStatus(id, status string) error {
	return a.Service.UpdatePendingActionStatus(id, status)
}

func (a *inboundPersistenceAdapter) RequestCrossChannelVerification(agentID, pendingActionID, agentUserID string) {
	body, _ := json.Marshal(map[string]interface{}{
		"pending_action_id":   pendingActionID,
		"source_user_id":      agentUserID,
		"source_channel_type": "unknown",
	})

	endpoint := fmt.Sprintf("%s/api/v1/internal/agent/%s/identity/request-verification", a.selfURL, agentID)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body)) // #nosec G107
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":          agentID,
			"pending_action_id": pendingActionID,
			"error":             err,
		}).Warn("inbound adapter: failed to request cross-channel verification")
		return
	}
	defer func() { _ = resp.Body.Close() }()
}

func (a *inboundPersistenceAdapter) TriggerIdentityMerge(agentID, verificationPAID string) {
	// Fetch the verification PA to get payload.
	pa, err := a.Service.GetAgentPendingActionByID(verificationPAID)
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
		_ = a.Service.UpdatePendingActionStatus(payload.OriginalPAID, "executed")
	}
	_ = a.Service.UpdatePendingActionStatus(verificationPAID, "executed")

	// Call merge.
	if err := a.Service.MergeAgentUsers(agentID, payload.SourceUserID, payload.TargetUserID); err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Warn("inbound adapter: identity merge failed")
	} else {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"source":   payload.SourceUserID,
			"target":   payload.TargetUserID,
		}).Info("identity merge completed")
	}
}
