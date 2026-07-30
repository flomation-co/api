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

func (s *Service) getProjects(c *gin.Context) {
	if !s.checkPermission(c, rbac.ProjectView) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	flat, err := s.persistence.GetProjects(user.ID, orgForUser(user))
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get projects")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, buildProjectTree(flat))
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
