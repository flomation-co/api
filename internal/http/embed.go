package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
)

// orgForUser returns the caller's active organisation id (first membership), or
// nil for a personal-scope caller — mirroring the environment handlers.
func orgForUser(user *api.User) *string {
	if user != nil && len(user.Organisations) > 0 {
		return &user.Organisations[0].ID
	}
	return nil
}

// embedResourceTypeValid gates the resource_type to the three embeddable kinds.
func embedResourceTypeValid(t string) bool {
	switch t {
	case api.EmbedResourceForm, api.EmbedResourceFlow, api.EmbedResourceAgent:
		return true
	default:
		return false
	}
}

// ── Embed app CRUD (JWT + RBAC gated) ────────────────────────────────

func (s *Service) listEmbedApps(c *gin.Context) {
	if !s.checkPermission(c, rbac.EmbedView) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	apps, err := s.persistence.ListEmbedApps(user.ID, orgForUser(user))
	if err != nil {
		log.WithError(err).Error("unable to list embed apps")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, apps)
}

type createEmbedAppBody struct {
	Name           string   `json:"name"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func (s *Service) createEmbedApp(c *gin.Context) {
	if !s.checkPermission(c, rbac.EmbedManage) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var body createEmbedAppBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	app := &api.EmbedApp{
		OwnerID:        user.ID,
		OrganisationID: orgForUser(user),
		Name:           body.Name,
	}
	created, err := s.persistence.CreateEmbedApp(app, body.AllowedOrigins)
	if err != nil {
		log.WithError(err).Error("unable to create embed app")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	// The publishable key is returned in full here — the create response is the
	// one place a client sees it (it's also safe to re-read, unlike a secret).
	c.JSON(http.StatusOK, created)
}

// loadOwnedEmbedApp fetches an embed app in the caller's scope, writing the
// appropriate status and returning nil when it can't be used.
func (s *Service) loadOwnedEmbedApp(c *gin.Context, perm rbac.Permission) *api.EmbedApp {
	if !s.checkPermission(c, perm) {
		return nil
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return nil
	}
	id := c.Param("id")
	if uuid.Validate(id) != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return nil
	}
	app, err := s.persistence.GetEmbedApp(id, user.ID, orgForUser(user))
	if err != nil {
		log.WithError(err).Error("unable to load embed app")
		c.AbortWithStatus(http.StatusBadRequest)
		return nil
	}
	if app == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return nil
	}
	return app
}

func (s *Service) getEmbedApp(c *gin.Context) {
	app := s.loadOwnedEmbedApp(c, rbac.EmbedView)
	if app == nil {
		return
	}
	c.JSON(http.StatusOK, app)
}

func (s *Service) deleteEmbedApp(c *gin.Context) {
	app := s.loadOwnedEmbedApp(c, rbac.EmbedManage)
	if app == nil {
		return
	}
	if _, err := s.persistence.DeleteEmbedApp(app.ID, app.OwnerID, app.OrganisationID); err != nil {
		log.WithError(err).Error("unable to delete embed app")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

type embedOriginBody struct {
	Origin string `json:"origin"`
}

func (s *Service) addEmbedOrigin(c *gin.Context) {
	app := s.loadOwnedEmbedApp(c, rbac.EmbedManage)
	if app == nil {
		return
	}
	var body embedOriginBody
	if err := c.BindJSON(&body); err != nil || body.Origin == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "origin is required"})
		return
	}
	if err := s.persistence.AddEmbedOrigin(app.ID, body.Origin); err != nil {
		log.WithError(err).Error("unable to add embed origin")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

func (s *Service) removeEmbedOrigin(c *gin.Context) {
	app := s.loadOwnedEmbedApp(c, rbac.EmbedManage)
	if app == nil {
		return
	}
	var body embedOriginBody
	if err := c.BindJSON(&body); err != nil || body.Origin == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "origin is required"})
		return
	}
	if err := s.persistence.RemoveEmbedOrigin(app.ID, body.Origin); err != nil {
		log.WithError(err).Error("unable to remove embed origin")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

type embedResourceBody struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Enabled      bool   `json:"enabled"`
}

// setEmbedResource opts a form/flow/agent in or out of an embed app.
func (s *Service) setEmbedResource(c *gin.Context) {
	app := s.loadOwnedEmbedApp(c, rbac.EmbedManage)
	if app == nil {
		return
	}
	var body embedResourceBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !embedResourceTypeValid(body.ResourceType) || uuid.Validate(body.ResourceID) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "resource_type must be form|flow|agent and resource_id a valid uuid"})
		return
	}
	if err := s.persistence.SetEmbedResource(app.ID, body.ResourceType, body.ResourceID, body.Enabled); err != nil {
		log.WithError(err).Error("unable to set embed resource")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

// ── Internal: publishable-key resolution for the Launch edge ─────────

type resolveEmbedKeyBody struct {
	PublishableKey string `json:"publishable_key"`
	Origin         string `json:"origin"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id"`
}

// resolveEmbedKey is called by Launch (internal, mTLS) to gate an embed request:
// it validates the publishable key and reports whether the presented Origin and
// target resource are permitted, in one round-trip. An unknown key returns 404
// (without revealing existence); the caller maps that to 401.
func (s *Service) resolveEmbedKey(c *gin.Context) {
	var body resolveEmbedKeyBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if body.PublishableKey == "" || !embedResourceTypeValid(body.ResourceType) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	res, err := s.persistence.ResolveEmbedKey(body.PublishableKey, body.Origin, body.ResourceType, body.ResourceID)
	if err != nil {
		log.WithError(err).Error("unable to resolve embed key")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if res == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, res)
}
