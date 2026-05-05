package http

// Phase 5 of the Agent Memory feature — HTTP handlers for identity linking.
//
// Route summary (registered in service.go):
//
//   POST   /internal/agent/:id/identity/lookup    check if identity exists
//   POST   /internal/agent/:id/identity/merge     merge two agent_user records
//   GET    /internal/agent/:id/pending-action/match  find pending action by type

import (
	"net/http"

	api "flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// --- Identity List ---

// listIdentitiesInternal handles GET /api/v1/internal/agent/:id/identity.
// Returns all identities for a given agent_user_id.
func (s *Service) listIdentitiesInternal(c *gin.Context) {
	agentUserID := c.Query("agent_user_id")
	if agentUserID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "agent_user_id required"})
		return
	}
	identities, err := s.persistence.GetAgentIdentitiesByUserID(agentUserID)
	if err != nil {
		log.WithError(err).Error("failed to list identities")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if identities == nil {
		identities = []*api.AgentIdentity{}
	}
	c.JSON(http.StatusOK, identities)
}

// --- Identity Lookup ---

type identityLookupRequest struct {
	ChannelType string `json:"channel_type" binding:"required"`
	ExternalID  string `json:"external_id" binding:"required"`
}

// lookupIdentityInternal handles POST /api/v1/internal/agent/:id/identity/lookup.
// Returns the identity and user if found, or 404 if the claimed identity
// has never been seen by this agent. This is the safety check that prevents
// identity spoofing — you can only link to identities the agent already knows.
func (s *Service) lookupIdentityInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body identityLookupRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	identity, user, err := s.persistence.LookupIdentity(agentID, body.ChannelType, body.ExternalID)
	if err != nil {
		log.WithError(err).Error("failed to lookup identity")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if identity == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "identity not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"identity": identity,
		"user":     user,
	})
}

// --- Identity Merge ---

type identityMergeRequest struct {
	SourceUserID string `json:"source_user_id" binding:"required"`
	TargetUserID string `json:"target_user_id" binding:"required"`
}

// mergeIdentityInternal handles POST /api/v1/internal/agent/:id/identity/merge.
// Transfers all identities, memories, commitments, and conversations from
// the source user to the target user, deduplicating memories by title.
// The source user is deleted after the merge.
func (s *Service) mergeIdentityInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body identityMergeRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if body.SourceUserID == body.TargetUserID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source and target must be different"})
		return
	}

	if err := s.persistence.MergeAgentUsers(agentID, body.SourceUserID, body.TargetUserID); err != nil {
		log.WithFields(log.Fields{
			"error":     err,
			"agent_id":  agentID,
			"source_id": body.SourceUserID,
			"target_id": body.TargetUserID,
		}).Error("failed to merge agent users")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	log.WithFields(log.Fields{
		"agent_id":  agentID,
		"source_id": body.SourceUserID,
		"target_id": body.TargetUserID,
	}).Info("agent users merged via identity link")

	c.Status(http.StatusNoContent)
}

// --- Pending Action Match ---

// matchPendingActionInternal handles GET /api/v1/internal/agent/:id/pending-action/match.
// Finds an open pending action of a given type for a user. Used by the
// confirmation processor when the extraction pipeline returns a confirmation
// without a specific pending_action_id.
func (s *Service) matchPendingActionInternal(c *gin.Context) {
	agentUserID := c.Query("agent_user_id")
	actionType := c.Query("type")

	if agentUserID == "" || actionType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_user_id and type are required"})
		return
	}

	pa, err := s.persistence.GetPendingActionByUserAndType(agentUserID, actionType)
	if err != nil {
		log.WithError(err).Error("failed to match pending action")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if pa == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching pending action found"})
		return
	}

	c.JSON(http.StatusOK, pa)
}
