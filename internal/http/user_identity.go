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
// authenticated user. organisation_id is optional: when omitted (or
// null) the declaration is personal-mode — used by personal agents.
// When provided, the user's membership in that org is verified against
// the JWT-derived user context.
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

	if body.ChannelType == "" || body.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_type and external_id are required"})
		return
	}

	// Org-scoped declarations must come from a member; personal-mode
	// declarations (nil org) are always allowed for the authenticated user.
	if body.OrganisationID != nil && *body.OrganisationID != "" {
		if !userBelongsToOrg(u, *body.OrganisationID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of that organisation"})
			return
		}
	} else {
		// Normalise empty-string org to nil so the COALESCE-based unique
		// index sees the same value the lookup queries pass in.
		body.OrganisationID = nil
	}

	body.UserID = u.ID

	created, err := s.persistence.CreateUserIdentity(body)
	if err != nil {
		log.WithFields(log.Fields{
			"error":        err,
			"user_id":      u.ID,
			"channel_type": body.ChannelType,
			"personal":     body.OrganisationID == nil,
		}).Error("unable to create user identity")
		// Conflict on existing tuple → 409 (editor distinguishes from real errors).
		c.JSON(http.StatusConflict, gin.H{"error": "identity already declared"})
		return
	}

	c.JSON(http.StatusCreated, created)
}

// deleteUserIdentity removes a single declared identity. Compound key
// supplied via query string because external_id may contain characters
// that are inconvenient in a URL path segment (e.g. Slack user IDs).
// Empty organisation_id query param targets a personal-mode declaration.
func (s *Service) deleteUserIdentity(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	organisationID := c.Query("organisation_id")
	channelType := c.Query("channel_type")
	externalID := c.Query("external_id")

	if channelType == "" || externalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_type and external_id query params are required"})
		return
	}

	var orgPtr *string
	if organisationID != "" {
		orgPtr = &organisationID
	}

	if err := s.persistence.DeleteUserIdentity(u.ID, orgPtr, channelType, externalID); err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"user_id":  u.ID,
			"personal": orgPtr == nil,
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
