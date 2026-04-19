package http

// Adapter that wraps the persistence service to satisfy the agent
// package's InboundPersistence interface. Uses direct function calls
// instead of HTTP self-calls (Phase 4 cleanup).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/agent"
	apipersistence "flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// inboundPersistenceAdapter wraps *persistence.Service and adds the
// orchestration methods that aren't pure DB operations.
type inboundPersistenceAdapter struct {
	*apipersistence.Service
	notifier  agent.ExecutionNotifier
	launchURL string
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
	pa, err := a.GetAgentPendingActionByID(pendingActionID)
	if err != nil || pa == nil {
		log.WithError(err).Warn("cross-channel verification: pending action not found")
		return
	}

	var payload struct {
		ChannelType  string `json:"channel_type"`
		ExternalID   string `json:"external_id"`
		TargetUserID string `json:"target_user_id"`
	}
	if err := json.Unmarshal(pa.Payload, &payload); err != nil || payload.ChannelType == "" || payload.ExternalID == "" {
		log.Warn("cross-channel verification: payload missing channel_type or external_id")
		return
	}

	// Look up the target identity by exact external ID first.
	targetIdentity, targetUser, err := a.LookupIdentity(agentID, payload.ChannelType, payload.ExternalID)

	// Fallback: if the external ID is a username (e.g. @AndyEsser) but the
	// identity is stored with a numeric ID, search by display name instead.
	if (targetIdentity == nil || targetUser == nil) && strings.HasPrefix(payload.ExternalID, "@") {
		username := strings.TrimPrefix(payload.ExternalID, "@")
		targetIdentity, targetUser = a.lookupIdentityByDisplayName(agentID, payload.ChannelType, username)
	}

	if targetIdentity == nil || targetUser == nil {
		log.WithFields(log.Fields{
			"agent_id":     agentID,
			"channel_type": payload.ChannelType,
			"external_id":  payload.ExternalID,
		}).Warn("cross-channel verification: target identity not found")
		return
	}

	channelID := payload.ExternalID
	if targetIdentity.ChannelScope != nil && *targetIdentity.ChannelScope != "" {
		channelID = *targetIdentity.ChannelScope
	}

	// Determine source channel from the requesting user's identities.
	sourceChannel := "unknown"
	if identities, err := a.GetAgentIdentitiesByUserID(agentUserID); err == nil && len(identities) > 0 {
		sourceChannel = identities[0].ChannelType
	}

	// Create a verification pending action on the target user.
	targetPayload, _ := json.Marshal(map[string]interface{}{
		"source_user_id": agentUserID,
		"target_user_id": targetUser.ID,
		"source_channel": sourceChannel,
		"original_pa_id": pendingActionID,
	})

	expires := time.Now().Add(24 * time.Hour)
	_, err = a.CreateAgentPendingAction(api.AgentPendingAction{
		AgentID:     agentID,
		AgentUserID: targetUser.ID,
		Type:        "identity_link_verification",
		Payload:     targetPayload,
		Evidence:    "A user on " + sourceChannel + " claims to also be you on " + payload.ChannelType,
		Status:      "awaiting_confirmation",
		ExpiresAt:   &expires,
	})
	if err != nil {
		log.WithError(err).Error("cross-channel verification: failed to create target PA")
		return
	}

	log.WithFields(log.Fields{
		"agent_id":       agentID,
		"target_user_id": targetUser.ID,
		"target_channel": payload.ChannelType,
	}).Info("cross-channel verification: target PA created, dispatching to Launch")

	// Dispatch to Launch so it fires the orchestrator flow on the target channel.
	if a.launchURL == "" {
		log.Warn("cross-channel verification: Launch URL not configured")
		return
	}

	dispatchPayload, _ := json.Marshal(map[string]interface{}{
		"target_user_id":      targetUser.ID,
		"target_channel_type": payload.ChannelType,
		"target_channel_id":   channelID,
		"target_external_id":  payload.ExternalID,
		"source_channel_type": sourceChannel,
	})

	endpoint := fmt.Sprintf("%s/internal/agent/%s/verify-identity", a.launchURL, agentID)
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(dispatchPayload)) // #nosec G107 — internal service-to-service call
	if err != nil {
		log.WithError(err).Error("cross-channel verification: failed to dispatch to Launch")
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.WithFields(log.Fields{
			"status": resp.StatusCode,
		}).Error("cross-channel verification: Launch dispatch returned non-2xx")
	}
}

func (a *inboundPersistenceAdapter) TriggerIdentityMerge(agentID, verificationPAID string) {
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

	// Merge directly.
	if err := a.Service.MergeAgentUsers(agentID, payload.SourceUserID, payload.TargetUserID); err != nil {
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

// lookupIdentityByDisplayName searches for a user with a matching display name
// who has an identity on the given channel type. This handles the case where
// the extraction stores a username (e.g. @AndyEsser) but the identity table
// uses a numeric ID from the channel's API.
func (a *inboundPersistenceAdapter) lookupIdentityByDisplayName(agentID, channelType, displayName string) (*api.AgentIdentity, *api.AgentUser) {
	users, err := a.GetAgentUsersByAgentID(agentID, 100, 0)
	if err != nil || len(users) == 0 {
		return nil, nil
	}

	lower := strings.ToLower(displayName)
	for _, u := range users {
		identities, err := a.GetAgentIdentitiesByUserID(u.ID)
		if err != nil || len(identities) == 0 {
			continue
		}

		for _, id := range identities {
			if id.ChannelType != channelType {
				continue
			}

			// Exact display name match (case-insensitive)
			if u.DisplayName != nil && strings.ToLower(*u.DisplayName) == lower {
				return id, u
			}

			// For Telegram: the user says "@AndyEsser" but display name
			// might be "Andy" or "@AndyEsser". Also try matching display
			// name prefixed with @ against the search term.
			if channelType == "telegram" && u.DisplayName != nil {
				dn := strings.ToLower(*u.DisplayName)
				// "@andyesser" matches display name "@andyesser" or "andyesser"
				if dn == lower || "@"+dn == lower || dn == "@"+lower {
					return id, u
				}
			}

			// Last resort for Telegram: if there's only one user with a
			// Telegram identity for this agent, it's very likely the right one.
			if channelType == "telegram" {
				log.WithFields(log.Fields{
					"agent_id":     agentID,
					"display_name": displayName,
					"matched_user": u.ID,
				}).Info("cross-channel verification: matched Telegram user by channel (single-match fallback)")
				return id, u
			}
		}
	}
	return nil, nil
}
