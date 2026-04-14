package http

// Phase 5 identity verification — proactive cross-channel dispatch.
//
// When the first side of an identity link confirms, the platform
// proactively reaches out on the target channel to request the second
// side's confirmation. This endpoint creates the target-side pending
// action and returns the channel details Launch needs to dispatch
// the orchestrator flow.
//
// Route: POST /api/v1/internal/agent/:id/identity/request-verification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type requestVerificationBody struct {
	PendingActionID string `json:"pending_action_id" binding:"required"`
	SourceUserID    string `json:"source_user_id" binding:"required"`
	SourceChannel   string `json:"source_channel_type" binding:"required"`
}

type verificationResponse struct {
	TargetUserID    string `json:"target_user_id"`
	TargetChannel   string `json:"target_channel_type"`
	TargetChannelID string `json:"target_channel_id"`
	TargetExternal  string `json:"target_external_id"`
	Private         bool   `json:"private"`
	PendingActionID string `json:"pending_action_id"`
}

// requestVerificationInternal handles POST /api/v1/internal/agent/:id/identity/request-verification.
//
// Steps:
//  1. Fetch the pending action to get the claimed identity details
//  2. Look up the target identity — must already exist (safety check)
//  3. Check whether the target channel is private
//  4. Create a matching PA under the target user
//  5. Return target channel details so Launch can dispatch the orchestrator
func (s *Service) requestVerificationInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body requestVerificationBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Fetch the original pending action.
	pa, err := s.persistence.GetAgentPendingActionByID(body.PendingActionID)
	if err != nil || pa == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pending action not found"})
		return
	}

	// Extract target channel details from the PA payload.
	var payload struct {
		ChannelType  string `json:"channel_type"`
		ExternalID   string `json:"external_id"`
		TargetUserID string `json:"target_user_id"`
	}
	if err := json.Unmarshal(pa.Payload, &payload); err != nil || payload.ChannelType == "" || payload.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pending action payload missing channel_type or external_id"})
		return
	}

	// Look up the target identity — safety check.
	targetIdentity, targetUser, err := s.persistence.LookupIdentity(agentID, payload.ChannelType, payload.ExternalID)
	if err != nil {
		log.WithError(err).Error("failed to look up target identity")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if targetIdentity == nil || targetUser == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "target identity not found — cannot verify unknown identity"})
		return
	}

	// Determine channel_id: prefer the identity's channel scope if set,
	// otherwise use the external_id (e.g. email address, Slack user DM).
	channelID := payload.ExternalID
	if targetIdentity.ChannelScope != nil && *targetIdentity.ChannelScope != "" {
		channelID = *targetIdentity.ChannelScope
	}

	// Check channel privacy.
	private := isPrivateChannel(payload.ChannelType, channelID)

	if !private {
		c.JSON(http.StatusOK, verificationResponse{
			TargetUserID:    targetUser.ID,
			TargetChannel:   payload.ChannelType,
			TargetChannelID: channelID,
			TargetExternal:  payload.ExternalID,
			Private:         false,
		})
		return
	}

	// Create a pending action under the TARGET user for the other-side verification.
	targetPayload, _ := json.Marshal(map[string]interface{}{
		"source_user_id":   body.SourceUserID,
		"target_user_id":   targetUser.ID,
		"source_channel":   body.SourceChannel,
		"original_pa_id":   body.PendingActionID,
	})

	expires := time.Now().Add(24 * time.Hour)
	targetPAID, err := s.persistence.CreateAgentPendingAction(api.AgentPendingAction{
		AgentID:     agentID,
		AgentUserID: targetUser.ID,
		Type:        "identity_link_verification",
		Payload:     targetPayload,
		Evidence:    "A user on " + body.SourceChannel + " claims to also be you on " + payload.ChannelType,
		Status:      "awaiting_confirmation",
		ExpiresAt:   &expires,
	})
	if err != nil {
		log.WithError(err).Error("failed to create target-side pending action")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	paID := ""
	if targetPAID != nil {
		paID = *targetPAID
	}

	// Dispatch the verification to Launch so it fires the orchestrator
	// flow on the target channel. This is async — we don't wait for it.
	go s.dispatchVerificationToLaunch(agentID, body.SourceChannel, payload.ChannelType, channelID, payload.ExternalID, targetUser.ID)

	c.JSON(http.StatusOK, verificationResponse{
		TargetUserID:    targetUser.ID,
		TargetChannel:   payload.ChannelType,
		TargetChannelID: channelID,
		TargetExternal:  payload.ExternalID,
		Private:         true,
		PendingActionID: paID,
	})
}

// dispatchVerificationToLaunch forwards the verification request to
// Launch's internal endpoint so it can fire the orchestrator flow on the
// target channel. Runs in a goroutine — errors are logged, not returned.
func (s *Service) dispatchVerificationToLaunch(agentID, sourceChannel, targetChannel, targetChannelID, targetExternal, targetUserID string) {
	launchURL := s.config.Launch.URL
	if launchURL == "" {
		log.Warn("identity verification: Launch URL not configured, cannot dispatch")
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"target_user_id":      targetUserID,
		"target_channel_type": targetChannel,
		"target_channel_id":   targetChannelID,
		"target_external_id":  targetExternal,
		"source_channel_type": sourceChannel,
	})

	endpoint := fmt.Sprintf("%s/internal/agent/%s/verify-identity", launchURL, agentID)
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(payload)) // #nosec G107 — internal service-to-service call
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Error("identity verification: failed to dispatch to Launch")
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.WithFields(log.Fields{
			"agent_id":   agentID,
			"status":     resp.StatusCode,
			"target":     targetChannel,
		}).Error("identity verification: Launch dispatch returned non-2xx")
	}
}

// isPrivateChannel returns true if the channel is a direct/private channel
// suitable for identity verification messages.
func isPrivateChannel(channelType, channelID string) bool {
	switch channelType {
	case "email":
		return true // email is always direct
	case "telegram":
		// Telegram private chats have positive numeric IDs; groups are negative.
		// If we can't determine, assume private (safer to send than not).
		if len(channelID) > 0 && channelID[0] == '-' {
			return false
		}
		return true
	case "slack":
		// Slack DM channels start with 'D'; group channels start with 'C' or 'G'.
		if len(channelID) > 0 && channelID[0] == 'D' {
			return true
		}
		return false
	default:
		return false
	}
}