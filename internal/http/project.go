package http

import (
	"errors"
	"net/http"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// buildProjectTree assembles a parent→children forest from a flat project list.
// Any node whose parent is missing from the set (e.g. filtered out) is treated
// as a root so nothing is silently dropped.
func buildProjectTree(flat []*api.Project) []*api.Project {
	byID := make(map[string]*api.Project, len(flat))
	for _, p := range flat {
		p.Children = nil
		byID[p.ID] = p
	}
	var roots []*api.Project
	for _, p := range flat {
		if p.ParentID != nil {
			if parent, ok := byID[*p.ParentID]; ok {
				parent.Children = append(parent.Children, p)
				continue
			}
		}
		roots = append(roots, p)
	}
	return roots
}

// isOrgAdmin reports whether the user is an admin of their active org. Admins
// bypass per-project restrictions (mirrors checkPermission's admin short-circuit).
func (s *Service) isOrgAdmin(user *api.User) bool {
	if user == nil || len(user.Organisations) == 0 {
		return false
	}
	role, err := s.persistence.GetUserRoleInOrganisation(user.Organisations[0].ID, user.ID)
	return err == nil && role != nil && *role == "admin"
}

func (s *Service) getProjects(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectView) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	flat, err := s.persistence.GetProjects(user.ID, orgForUser(user), s.isOrgAdmin(user))
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get projects")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, buildProjectTree(flat))
}

// requireProjectManageAccess enforces per-project access on manage actions: a
// restricted project may only be managed by a user with the 'manage' effective
// role on it (owner/admin resolve to 'manage'). Open projects are governed by
// the org-level ProjectManage permission alone. Returns true when allowed.
func (s *Service) requireProjectManageAccess(c *gin.Context, user *api.User, projectID string) bool {
	access, err := s.persistence.GetProjectAccess(user.ID, orgForUser(user), s.isOrgAdmin(user))
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return false
	}
	a := access[projectID]
	if !a.Accessible || (a.Restricted && a.Role != "manage") {
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}
	return true
}

func (s *Service) getProjectACL(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectView) {
		return
	}
	user := s.getUserFromContext(c)
	id := c.Param("id")

	project, err := s.persistence.GetProjectByID(id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if project == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.verifyOrgAccess(user, project.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	direct, inherited, err := s.persistence.GetProjectACL(id)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get project acl")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, gin.H{"direct": direct, "inherited": inherited})
}

func (s *Service) setProjectACL(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectManage) {
		return
	}
	user := s.getUserFromContext(c)
	id := c.Param("id")

	project, err := s.persistence.GetProjectByID(id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if project == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.verifyOrgAccess(user, project.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if !s.requireProjectManageAccess(c, user, id) {
		return
	}

	var body struct {
		GroupID string  `json:"group_id"`
		Role    *string `json:"role"` // null/"" → remove the grant
	}
	if err := c.BindJSON(&body); err != nil || body.GroupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_id is required"})
		return
	}

	if body.Role == nil || *body.Role == "" {
		if err := s.persistence.RemoveProjectGroup(id, body.GroupID); err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
		return
	}

	switch *body.Role {
	case "view", "edit", "manage":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be view, edit or manage"})
		return
	}

	if err := s.persistence.SetProjectGroupRole(id, body.GroupID, *body.Role); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

type projectBody struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ParentID    *string `json:"parent_id"`
}

func (s *Service) createProject(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectCreate) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	org := orgForUser(user)

	var body projectBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	// A parent must exist and be in the caller's scope.
	if body.ParentID != nil {
		parent, err := s.persistence.GetProjectByID(*body.ParentID)
		if err != nil || parent == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent project not found"})
			return
		}
		if !s.verifyOrgAccess(user, parent.OrganisationID) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	p := api.Project{
		Name:           body.Name,
		Description:    body.Description,
		ParentID:       body.ParentID,
		OrganisationID: org,
		OwnerID:        &user.ID,
	}
	id, err := s.persistence.CreateProject(p)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create project")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	created, err := s.persistence.GetProjectByID(*id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (s *Service) updateProject(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectManage) {
		return
	}
	user := s.getUserFromContext(c)
	id := c.Param("id")

	existing, err := s.persistence.GetProjectByID(id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.verifyOrgAccess(user, existing.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if !s.requireProjectManageAccess(c, user, id) {
		return
	}

	var body projectBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	name := body.Name
	if name == "" {
		name = existing.Name
	}

	// A new parent must be in scope too.
	if body.ParentID != nil {
		parent, err := s.persistence.GetProjectByID(*body.ParentID)
		if err != nil || parent == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent project not found"})
			return
		}
		if !s.verifyOrgAccess(user, parent.OrganisationID) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	if err := s.persistence.UpdateProject(id, name, body.Description, body.ParentID); err != nil {
		if errors.Is(err, persistence.ErrProjectCycle) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		log.WithFields(log.Fields{"error": err}).Error("unable to update project")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	updated, _ := s.persistence.GetProjectByID(id)
	c.JSON(http.StatusOK, updated)
}

func (s *Service) deleteProject(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectManage) {
		return
	}
	user := s.getUserFromContext(c)
	id := c.Param("id")

	existing, err := s.persistence.GetProjectByID(id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.verifyOrgAccess(user, existing.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if !s.requireProjectManageAccess(c, user, id) {
		return
	}

	if err := s.persistence.ArchiveProject(id); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to archive project")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

// moveFlosToProject assigns the given flows to a project (or ungroups them when
// project_id is null). Registered under the flo group.
func (s *Service) moveFlosToProject(c *gin.Context) {
	if !s.checkPermission(c, rbac.FlowEdit) {
		return
	}
	user := s.getUserFromContext(c)

	var body struct {
		FloIDs    []string `json:"flo_ids"`
		ProjectID *string  `json:"project_id"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if len(body.FloIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "flo_ids is required"})
		return
	}

	// A target project must exist and be in scope.
	if body.ProjectID != nil {
		project, err := s.persistence.GetProjectByID(*body.ProjectID)
		if err != nil || project == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project not found"})
			return
		}
		if !s.verifyOrgAccess(user, project.OrganisationID) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	if err := s.persistence.MoveFlosToProject(body.FloIDs, body.ProjectID, user.ID, orgForUser(user)); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to move flows to project")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}
