package http

import (
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// upsertUserIdentityInternal handles POST /api/v1/internal/user-identity
// — the destination for Launch's identity-OAuth callback (R3 Phase 2).
// Writes a user_identity row using the user_id resolved server-side by
// Launch (from the JWT-cookie session that initiated the OAuth flow), so
// no client-supplied user_id is trusted — the OAuth state machine is the
// authority.
//
// Request body shape (sent by Launch):
//
//	{
//	  "user_id": "<uuid>",         // required — from Launch's session validation
//	  "channel_type": "email",     // required — picked when the user clicked "Connect"
//	  "external_id": "...",        // required — derived from the provider's userinfo
//	  "display_name": "...",       // optional
//	  "organisation_id": "<uuid>"  // optional — absent = personal mode
//	}
//
// 409 on duplicate (idempotent re-OAuth from the same provider).
func (s *Service) upsertUserIdentityInternal(c *gin.Context) {
	var body api.CreateUserIdentity
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if body.UserID == "" || body.ChannelType == "" || body.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id, channel_type, and external_id are required"})
		return
	}

	// Normalise empty-string org to nil so the COALESCE-based unique
	// index and IS NOT DISTINCT FROM lookup queries see the same value.
	if body.OrganisationID != nil && *body.OrganisationID == "" {
		body.OrganisationID = nil
	}

	created, err := s.persistence.CreateUserIdentity(body)
	if err != nil {
		log.WithFields(log.Fields{
			"error":        err,
			"user_id":      body.UserID,
			"channel_type": body.ChannelType,
			"personal":     body.OrganisationID == nil,
		}).Error("internal: unable to create user identity from OAuth callback")
		// Idempotent — duplicate is fine; surface as 409 so Launch logs but
		// doesn't fail the user-facing OAuth completion.
		c.JSON(http.StatusConflict, gin.H{"error": "identity already declared"})
		return
	}

	c.JSON(http.StatusCreated, created)
}
