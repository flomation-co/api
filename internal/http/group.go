package http

import (
	"net/http"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getOrganisationGroups(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")

	groups, err := s.persistence.GetGroupsByOrganisationID(orgID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get groups")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(groups) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, groups)
}

func (s *Service) getGroupByID(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	group, err := s.persistence.GetGroupByID(groupID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if group == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, group)
}

type CreateGroupRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsDefault   bool    `json:"is_default"`
}

func (s *Service) createOrganisationGroup(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	orgID := c.Param("ID")

	var req CreateGroupRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	group := api.Group{
		OrganisationID: orgID,
		Name:           req.Name,
		Description:    req.Description,
		IsDefault:      req.IsDefault,
	}

	id, err := s.persistence.CreateGroup(group)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	created, err := s.persistence.GetGroupByID(*id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, created)
}

type UpdateGroupRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsDefault   bool    `json:"is_default"`
}

func (s *Service) updateGroup(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	var req UpdateGroupRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	group := api.Group{
		ID:        groupID,
		Name:      req.Name,
		IsDefault: req.IsDefault,
	}
	group.Description = req.Description

	if err := s.persistence.UpdateGroup(group); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to update group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	updated, err := s.persistence.GetGroupByID(groupID)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, updated)
}

func (s *Service) deleteGroup(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	if err := s.persistence.DeleteGroup(groupID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to delete group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) getGroupMembers(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	members, err := s.persistence.GetGroupMembers(groupID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get group members")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	agents, err := s.persistence.GetAgentGroupMembers(groupID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get agent group members")
		agents = []*api.AgentGroupMember{}
	}

	if len(members) == 0 && len(agents) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"users":  members,
		"agents": agents,
	})
}

type AddGroupMemberRequest struct {
	UserID string `json:"user_id"`
}

func (s *Service) addGroupMember(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	var req AddGroupMemberRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.AddUserToGroup(groupID, req.UserID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to add member to group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusCreated)
}

func (s *Service) removeGroupMember(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")
	userID := c.Param("userID")

	if err := s.persistence.RemoveUserFromGroup(groupID, userID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to remove member from group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

type SetPermissionsRequest struct {
	Permissions []string `json:"permissions"`
}

func (s *Service) setGroupPermissions(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	var req SetPermissionsRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Validate all permissions
	for _, perm := range req.Permissions {
		if !rbac.IsValidPermission(perm) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":              "invalid_permission",
				"invalid_permission": perm,
			})
			return
		}
	}

	if err := s.persistence.SetGroupPermissions(groupID, req.Permissions); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to set group permissions")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	group, err := s.persistence.GetGroupByID(groupID)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, group)
}

type AddAgentToGroupRequest struct {
	AgentID string `json:"agent_id"`
}

func (s *Service) addAgentToGroup(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")

	var req AddAgentToGroupRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.AddAgentToGroup(groupID, req.AgentID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to add agent to group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) removeAgentFromGroup(c *gin.Context) {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return
	}

	groupID := c.Param("groupID")
	agentID := c.Param("agentID")

	if err := s.persistence.RemoveAgentFromGroup(groupID, agentID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to remove agent from group")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}
