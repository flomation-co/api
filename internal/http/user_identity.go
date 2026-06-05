package http

import (
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// listUserIdentities returns every declared identity for the
// authenticated user, across all their organisations. The editor profile
// UI groups the response by organisation for display.
func (s *Service) listUserIdentities(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	identities, err := s.persistence.GetUserIdentitiesByUserID(u.ID)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "user_id": u.ID}).Error("unable to list user identities")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if identities == nil {
		identities = []*api.UserIdentity{}
	}

	c.JSON(http.StatusOK, identities)
}

// createUserIdentity declares a new channel identity for the
// authenticated user in a specific organisation. The request body must
// supply organisation_id, channel_type, and external_id; display_name is
// optional. The user's membership in the organisation is verified
// against the JWT-derived user context.
func (s *Service) createUserIdentity(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var body api.CreateUserIdentity
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	if body.OrganisationID == "" || body.ChannelType == "" || body.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "organisation_id, channel_type, and external_id are required"})
		return
	}

	// Ensure the caller is actually a member of the org they claim to
	// be declaring an identity in.
	if !userBelongsToOrg(u, body.OrganisationID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of that organisation"})
		return
	}

	body.UserID = u.ID

	created, err := s.persistence.CreateUserIdentity(body)
	if err != nil {
		log.WithFields(log.Fields{
			"error":           err,
			"user_id":         u.ID,
			"organisation_id": body.OrganisationID,
			"channel_type":    body.ChannelType,
		}).Error("unable to create user identity")
		// Conflict on existing (user, org, channel, external) tuple is
		// fine to treat as idempotent — surface as 409 so the editor can
		// distinguish from real errors.
		c.JSON(http.StatusConflict, gin.H{"error": "identity already declared"})
		return
	}

	c.JSON(http.StatusCreated, created)
}

// deleteUserIdentity removes a single declared identity. Compound key
// supplied via query string because external_id may contain characters
// that are inconvenient in a URL path segment (e.g. Slack user IDs).
func (s *Service) deleteUserIdentity(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	organisationID := c.Query("organisation_id")
	channelType := c.Query("channel_type")
	externalID := c.Query("external_id")

	if organisationID == "" || channelType == "" || externalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "organisation_id, channel_type, and external_id query params are required"})
		return
	}

	if err := s.persistence.DeleteUserIdentity(u.ID, organisationID, channelType, externalID); err != nil {
		log.WithFields(log.Fields{
			"error":           err,
			"user_id":         u.ID,
			"organisation_id": organisationID,
		}).Error("unable to delete user identity")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

// userBelongsToOrg returns true when the given user is a member of the
// given organisation. The user struct returned by getUserFromContext
// embeds an Organisations slice populated at token-exchange time.
func userBelongsToOrg(u *api.User, organisationID string) bool {
	for _, o := range u.Organisations {
		if o.ID == organisationID {
			return true
		}
	}
	return false
}
