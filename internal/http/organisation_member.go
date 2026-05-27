package http

import (
	"net/http"

	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getOrganisationMembers(c *gin.Context) {
	orgID := c.Param("ID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Verify caller is a member
	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	members, err := s.persistence.GetOrganisationMembers(orgID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get organisation members")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	agents, err := s.persistence.GetOrganisationAgents(orgID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Warn("unable to get organisation agents")
	}

	if len(members) == 0 && len(agents) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"members": members,
		"agents":  agents,
	})
}

func (s *Service) removeOrganisationMember(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")
	targetUserID := c.Param("userID")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Cannot remove yourself
	if targetUserID == user.ID {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.RemoveUserFromOrganisation(orgID, targetUserID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to remove user from organisation")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}
