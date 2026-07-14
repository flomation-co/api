package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"flomation.app/automate/api/internal/utils"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// gatewayAuthTypes is the closed set of pluggable authenticator types a Gateway
// API can use. "open" is the default; api_key/basic carry a hashed secret; oidc
// and flomation are secretless (verified against a JWKS / the Sentinel session).
var gatewayAuthTypes = map[string]struct{}{
	"open": {}, "api_key": {}, "basic": {}, "oidc": {}, "flomation": {},
}

func gatewayAuthTypeValid(t string) bool {
	_, ok := gatewayAuthTypes[t]
	return ok
}

// gatewaySecretSaltLen is the salt length for api_key/basic secret hashing.
const gatewaySecretSaltLen = 16

// hashGatewaySecret computes the salted SHA-256 (hex) of a Gateway auth secret.
// Only the hash + salt are stored; the plaintext is shown once at creation and
// never persisted. Launch receives the hash+salt over mTLS and compares.
func hashGatewaySecret(secret, salt string) string {
	sum := sha256.Sum256([]byte(salt + secret))
	return hex.EncodeToString(sum[:])
}

type gatewayAuthBody struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
	// Secret is the plaintext api_key value / basic password — write-only, hashed
	// on receipt. Absent on update ⇒ keep the existing secret.
	Secret string `json:"secret"`
}

type createGatewayAPIBody struct {
	Name string           `json:"name"`
	Auth *gatewayAuthBody `json:"auth"`
}

// applyGatewayAuth validates the auth body and folds it onto the api struct,
// returning the (hash, salt) to persist — both nil when there's no secret to set
// (leave existing untouched). A secret is required the FIRST time api_key/basic
// is selected; the caller decides whether that's create (required) or update.
func applyGatewayAuth(a *api.GatewayAPI, body *gatewayAuthBody, requireSecret bool) (*string, *string, string) {
	if body == nil {
		a.AuthType = "open"
		a.AuthConfig = json.RawMessage("{}")
		return nil, nil, ""
	}
	t := strings.TrimSpace(body.Type)
	if t == "" {
		t = "open"
	}
	if !gatewayAuthTypeValid(t) {
		return nil, nil, "unsupported auth type"
	}
	a.AuthType = t
	if len(body.Config) == 0 {
		a.AuthConfig = json.RawMessage("{}")
	} else {
		a.AuthConfig = body.Config
	}
	needsSecret := t == "api_key" || t == "basic"
	if needsSecret && body.Secret != "" {
		salt := utils.GenerateRandomStringID(gatewaySecretSaltLen)
		hash := hashGatewaySecret(body.Secret, salt)
		return &hash, &salt, ""
	}
	if needsSecret && requireSecret {
		return nil, nil, "a secret is required for this auth type"
	}
	return nil, nil, ""
}

func (s *Service) listGatewayAPIs(c *gin.Context) {
	if !s.checkPermission(c, rbac.GatewayView) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	apis, err := s.persistence.ListGatewayAPIs(user.ID, orgForUser(user))
	if err != nil {
		log.WithError(err).Error("unable to list gateway apis")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, apis)
}

func (s *Service) createGatewayAPI(c *gin.Context) {
	if !s.checkPermission(c, rbac.GatewayManage) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var body createGatewayAPIBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	a := &api.GatewayAPI{OwnerID: user.ID, OrganisationID: orgForUser(user), Name: body.Name}
	hash, salt, verr := applyGatewayAuth(a, body.Auth, true)
	if verr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": verr})
		return
	}
	a.AuthSecretHash, a.AuthSecretSalt = hash, salt
	created, err := s.persistence.CreateGatewayAPI(a)
	if err != nil {
		log.WithError(err).Error("unable to create gateway api")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, created)
}

// loadOwnedGatewayAPI fetches a gateway api in the caller's scope, writing the
// appropriate status and returning nil when it can't be used.
func (s *Service) loadOwnedGatewayAPI(c *gin.Context, perm rbac.Permission) *api.GatewayAPI {
	if !s.checkPermission(c, perm) {
		return nil
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return nil
	}
	a, err := s.persistence.GetGatewayAPI(c.Param("id"), user.ID, orgForUser(user))
	if err != nil {
		log.WithError(err).Error("unable to load gateway api")
		c.AbortWithStatus(http.StatusBadRequest)
		return nil
	}
	if a == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return nil
	}
	return a
}

func (s *Service) getGatewayAPI(c *gin.Context) {
	a := s.loadOwnedGatewayAPI(c, rbac.GatewayView)
	if a == nil {
		return
	}
	c.JSON(http.StatusOK, a)
}

func (s *Service) updateGatewayAPI(c *gin.Context) {
	a := s.loadOwnedGatewayAPI(c, rbac.GatewayManage)
	if a == nil {
		return
	}
	user := s.getUserFromContext(c)
	var body createGatewayAPIBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) != "" {
		a.Name = body.Name
	}
	// On update a secret is optional (nil hash/salt ⇒ keep the existing one).
	hash, salt, verr := applyGatewayAuth(a, body.Auth, false)
	if verr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": verr})
		return
	}
	ok, err := s.persistence.UpdateGatewayAPI(a, user.ID, orgForUser(user), hash, salt)
	if err != nil {
		log.WithError(err).Error("unable to update gateway api")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Service) deleteGatewayAPI(c *gin.Context) {
	if !s.checkPermission(c, rbac.GatewayManage) {
		return
	}
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	ok, err := s.persistence.DeleteGatewayAPI(c.Param("id"), user.ID, orgForUser(user))
	if err != nil {
		log.WithError(err).Error("unable to delete gateway api")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}

type gatewayEndpointBody struct {
	Method      string `json:"method"`
	PathPattern string `json:"path_pattern"`
	FlowID      string `json:"flow_id"`
	TriggerID   string `json:"trigger_id"`
	Enabled     *bool  `json:"enabled"`
}

func gatewayMethodValid(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// resolveWebTriggerID returns the flow's "web" trigger record id, so the editor
// only has to pick a flow — the endpoint's trigger_id is derived here. Empty when
// the flow has no Web Trigger (the caller rejects the endpoint).
func (s *Service) resolveWebTriggerID(flowID string) string {
	triggers, err := s.persistence.GetTriggersByFloID(flowID)
	if err != nil {
		return ""
	}
	for _, t := range triggers {
		if t != nil && t.TypeName == "web" {
			return t.ID
		}
	}
	return ""
}

func (s *Service) createGatewayEndpoint(c *gin.Context) {
	a := s.loadOwnedGatewayAPI(c, rbac.GatewayManage)
	if a == nil {
		return
	}
	var body gatewayEndpointBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !gatewayMethodValid(body.Method) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid HTTP method"})
		return
	}
	if !strings.HasPrefix(body.PathPattern, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path_pattern must start with /"})
		return
	}
	if body.FlowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "flow_id is required"})
		return
	}
	// The editor only sends flow_id; derive the flow's Web Trigger here.
	triggerID := body.TriggerID
	if triggerID == "" {
		triggerID = s.resolveWebTriggerID(body.FlowID)
	}
	if triggerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the selected flow has no Web Trigger"})
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	ep := &api.GatewayEndpoint{
		GatewayAPIID: a.ID,
		Method:       body.Method,
		PathPattern:  body.PathPattern,
		FlowID:       body.FlowID,
		TriggerID:    triggerID,
		Enabled:      enabled,
	}
	created, err := s.persistence.CreateGatewayEndpoint(ep)
	if err != nil {
		log.WithError(err).Error("unable to create gateway endpoint")
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create endpoint (duplicate method+path?)"})
		return
	}
	c.JSON(http.StatusOK, created)
}

func (s *Service) updateGatewayEndpoint(c *gin.Context) {
	a := s.loadOwnedGatewayAPI(c, rbac.GatewayManage)
	if a == nil {
		return
	}
	var body gatewayEndpointBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !gatewayMethodValid(body.Method) || !strings.HasPrefix(body.PathPattern, "/") || body.FlowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid endpoint"})
		return
	}
	triggerID := body.TriggerID
	if triggerID == "" {
		triggerID = s.resolveWebTriggerID(body.FlowID)
	}
	if triggerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the selected flow has no Web Trigger"})
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	ep := &api.GatewayEndpoint{
		ID:           c.Param("eid"),
		GatewayAPIID: a.ID,
		Method:       body.Method,
		PathPattern:  body.PathPattern,
		FlowID:       body.FlowID,
		TriggerID:    triggerID,
		Enabled:      enabled,
	}
	ok, err := s.persistence.UpdateGatewayEndpoint(ep)
	if err != nil {
		log.WithError(err).Error("unable to update gateway endpoint")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Service) deleteGatewayEndpoint(c *gin.Context) {
	a := s.loadOwnedGatewayAPI(c, rbac.GatewayManage)
	if a == nil {
		return
	}
	ok, err := s.persistence.DeleteGatewayEndpoint(c.Param("eid"), a.ID)
	if err != nil {
		log.WithError(err).Error("unable to delete gateway endpoint")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}

// ── Internal (mTLS) endpoints — called by the Launch edge ──

// resolveGatewayAPIInternal returns the API's auth policy + endpoints for a short
// api_id. GET /api/v1/internal/gateway/:apiId/resolve
func (s *Service) resolveGatewayAPIInternal(c *gin.Context) {
	res, err := s.persistence.ResolveGatewayAPI(c.Param("apiId"))
	if err != nil {
		log.WithError(err).Error("unable to resolve gateway api")
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if res == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, res)
}

type verifyGatewaySessionBody struct {
	Token              string `json:"token"`
	OrganisationID     string `json:"organisation_id"`
	OwnerID            string `json:"owner_id"`
	RequiredPermission string `json:"required_permission"`
}

type verifyGatewaySessionResult struct {
	OK     bool   `json:"ok"`
	UserID string `json:"user_id,omitempty"`
}

// verifyGatewaySessionInternal validates a Flomation (Sentinel) session token for
// the "flomation" gateway auth type: it resolves the user, confirms org
// membership (or personal ownership), and enforces the configured RBAC
// permission. POST /api/v1/internal/gateway/:apiId/verify-session
func (s *Service) verifyGatewaySessionInternal(c *gin.Context) {
	var body verifyGatewaySessionBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: false})
		return
	}
	uid, err := resolveUserFromToken(s.config.Security.IdentityService, token)
	if err != nil || uid == "" {
		c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: false})
		return
	}
	// Personal-scoped API: only the owner may call it.
	if body.OrganisationID == "" {
		if uid != body.OwnerID {
			c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: false})
			return
		}
		c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: true, UserID: uid})
		return
	}
	// Org-scoped API: the user must be a member (has effective permissions in the
	// org) and, when configured, hold the required permission.
	perms, err := s.persistence.GetUserPermissionsInOrganisation(body.OrganisationID, uid)
	if err != nil {
		log.WithError(err).Error("gateway verify-session: permission lookup failed")
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if len(perms) == 0 {
		c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: false}) // not a member
		return
	}
	if req := strings.TrimSpace(body.RequiredPermission); req != "" {
		if !rbac.HasPermission(perms, rbac.Permission(req)) {
			c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: false})
			return
		}
	}
	c.JSON(http.StatusOK, verifyGatewaySessionResult{OK: true, UserID: uid})
}
