package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type CreateInviteRequest struct {
	Email *string `json:"email"`
	Role  string  `json:"role"`
}

func (s *Service) createOrganisationInvite(c *gin.Context) {
	orgID := c.Param("ID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Only admins can create invites
	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
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
	orgID := c.Param("ID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Only admins can view invites
	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

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
	orgID := c.Param("ID")
	inviteID := c.Param("inviteID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Only admins can revoke invites
	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if err := s.persistence.RevokeInvite(inviteID, orgID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to revoke invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
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
	if err := s.persistence.AddUserToOrganisation(invite.OrganisationID, user.ID, invite.Role); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to add user to organisation")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Mark invite as accepted
	if err := s.persistence.AcceptInvite(invite.ID, user.ID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to accept invite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	org, err := s.persistence.GetOrganisationByID(invite.OrganisationID)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, org)
}
