package http

import (
	"net/http"

	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type CreateInviteRequest struct {
	Email *string `json:"email"`
	Role  string  `json:"role"`
}

func (s *Service) createOrganisationInvite(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req CreateInviteRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if req.Role == "" {
		req.Role = "member"
	}

	invite, err := s.persistence.CreateOrganisationInvite(orgID, req.Email, req.Role, user.ID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create organisation invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, invite)
}

func (s *Service) getOrganisationInvites(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")

	invites, err := s.persistence.GetOrganisationInvites(orgID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get organisation invites")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(invites) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, invites)
}

func (s *Service) revokeOrganisationInvite(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")
	inviteID := c.Param("inviteID")

	if err := s.persistence.RevokeInvite(inviteID, orgID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to revoke invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) getInvitePreview(c *gin.Context) {
	code := c.Param("code")

	preview, err := s.persistence.GetInvitePreview(code)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get invite preview")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if preview == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, preview)
}

func (s *Service) acceptOrganisationInvite(c *gin.Context) {
	code := c.Param("code")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	invite, err := s.persistence.GetInviteByCode(code)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if invite == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Add user to the organisation with the invite's role
	// Ignore duplicate membership errors (user may already be a member)
	if err := s.persistence.AddUserToOrganisation(invite.OrganisationID, user.ID, invite.Role); err != nil {
		log.WithFields(log.Fields{
			"error":           err,
			"organisation_id": invite.OrganisationID,
			"user_id":         user.ID,
		}).Warn("unable to add user to organisation (may already be a member)")
	}

	// Mark invite as accepted
	if err := s.persistence.AcceptInvite(invite.ID, user.ID); err != nil {
		log.WithFields(log.Fields{
			"error":     err,
			"invite_id": invite.ID,
			"user_id":   user.ID,
		}).Error("unable to accept invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Auto-add user to default groups
	defaultGroups, err := s.persistence.GetDefaultGroupsForOrganisation(invite.OrganisationID)
	if err == nil {
		for _, groupID := range defaultGroups {
			if err := s.persistence.AddUserToGroup(groupID, user.ID); err != nil {
				log.WithFields(log.Fields{
					"error":    err,
					"group_id": groupID,
					"user_id":  user.ID,
				}).Warn("unable to add user to default group")
			}
		}
	}

	org, err := s.persistence.GetOrganisationByID(invite.OrganisationID)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, org)
}
