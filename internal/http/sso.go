package http

import (
	"encoding/json"
	"net/http"

	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// orgManageGuard enforces that the caller is an admin/OrganisationManage of the
// org named in the route AND that it's their active org, then returns the org id.
// Returns "" (and aborts) when not allowed.
func (s *Service) orgManageGuard(c *gin.Context) string {
	if !s.checkPermission(c, rbac.OrganisationManage) {
		return ""
	}
	orgID := c.Param("ID")
	user := s.getUserFromContext(c)
	if user == nil || len(user.Organisations) == 0 || user.Organisations[0].ID != orgID {
		c.AbortWithStatus(http.StatusForbidden)
		return ""
	}
	return orgID
}

// ssoOrgGuard is orgManageGuard plus a check that the Sentinel service link is
// wired — used by the connection/domain forwards (which call Sentinel). The
// group-mapping endpoints are API-local and use orgManageGuard directly.
func (s *Service) ssoOrgGuard(c *gin.Context) string {
	orgID := s.orgManageGuard(c)
	if orgID == "" {
		return ""
	}
	if s.config.Security.ServiceToken == "" || s.config.Security.IdentityService == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SSO is not configured on this server (missing service_token / identity_service link to Sentinel)"})
		return ""
	}
	return orgID
}

// ── Group → Team mappings (API-local; no Sentinel involved) ──────────

func (s *Service) listSSOGroupMappings(c *gin.Context) {
	orgID := s.orgManageGuard(c)
	if orgID == "" {
		return
	}
	m, err := s.persistence.GetSSOGroupMappings(orgID)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, m)
}

func (s *Service) createSSOGroupMapping(c *gin.Context) {
	orgID := s.orgManageGuard(c)
	if orgID == "" {
		return
	}
	var body struct {
		IDPGroup            string `json:"idp_group"`
		OrganisationGroupID string `json:"organisation_group_id"`
	}
	if err := c.BindJSON(&body); err != nil || body.IDPGroup == "" || body.OrganisationGroupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "idp_group and organisation_group_id are required"})
		return
	}
	if err := s.persistence.CreateSSOGroupMapping(orgID, body.IDPGroup, body.OrganisationGroupID); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusCreated)
}

func (s *Service) deleteSSOGroupMapping(c *gin.Context) {
	orgID := s.orgManageGuard(c)
	if orgID == "" {
		return
	}
	if err := s.persistence.DeleteSSOGroupMapping(c.Param("mappingID"), orgID); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

// reconcileSSOGroupsInternal is called by Sentinel after login with the user's
// resolved IdP groups; token-guarded. Syncs the user's Team memberships.
func (s *Service) reconcileSSOGroupsInternal(c *gin.Context) {
	var body struct {
		UserID         string   `json:"user_id"`
		OrganisationID string   `json:"organisation_id"`
		IDPGroups      []string `json:"idp_groups"`
	}
	if err := c.BindJSON(&body); err != nil || body.UserID == "" || body.OrganisationID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if err := s.persistence.ReconcileSSOGroups(body.OrganisationID, body.UserID, body.IDPGroups); err != nil {
		log.WithField("error", err).Error("sso reconcile groups")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)
}

// serviceTokenGuardAPI protects the SSO service-to-service endpoints Sentinel
// calls after a login (membership sync). Shared secret; refused when unset.
func (s *Service) serviceTokenGuardAPI(c *gin.Context) {
	want := s.config.Security.ServiceToken
	got := c.GetHeader("X-Service-Token")
	if want == "" || got == "" || got != want {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Next()
}

// ensureOrgMembershipInternal is called by Sentinel after a successful SSO login
// to JIT the user into the connection's organisation. It never downgrades an
// existing role — it only adds the user as a member when they aren't one yet.
func (s *Service) ensureOrgMembershipInternal(c *gin.Context) {
	var body struct {
		UserID         string `json:"user_id"`
		OrganisationID string `json:"organisation_id"`
	}
	if err := c.BindJSON(&body); err != nil || body.UserID == "" || body.OrganisationID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	role, err := s.persistence.GetUserRoleInOrganisation(body.OrganisationID, body.UserID)
	if err != nil {
		log.WithField("error", err).Error("sso ensure-membership: role lookup")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if role == nil {
		if err := s.persistence.AddUserToOrganisation(body.OrganisationID, body.UserID, "member"); err != nil {
			log.WithField("error", err).Error("sso ensure-membership: add member")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	c.Status(http.StatusOK)
}

// raw forwards a Sentinel admin-API JSON body straight through to the client.
func raw(c *gin.Context, data json.RawMessage, err error) {
	if err != nil {
		log.WithField("error", err).Error("sso admin call failed")
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}
	c.Data(http.StatusOK, "application/json", data)
}

func (s *Service) getSSORedirectURI(c *gin.Context) {
	if s.ssoOrgGuard(c) == "" {
		return
	}
	data, err := s.ssoSentinel.RedirectURI()
	raw(c, data, err)
}

func (s *Service) listSSOConnections(c *gin.Context) {
	orgID := s.ssoOrgGuard(c)
	if orgID == "" {
		return
	}
	data, err := s.ssoSentinel.ListConnections(orgID)
	raw(c, data, err)
}

func (s *Service) createSSOConnection(c *gin.Context) {
	orgID := s.ssoOrgGuard(c)
	if orgID == "" {
		return
	}
	var body map[string]interface{}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	body["organisation_id"] = orgID // never trust a client-supplied org
	id, err := s.ssoSentinel.CreateConnection(body)
	if err != nil {
		log.WithField("error", err).Error("create sso connection")
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (s *Service) updateSSOConnection(c *gin.Context) {
	orgID := s.ssoOrgGuard(c)
	if orgID == "" {
		return
	}
	var body map[string]interface{}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	body["organisation_id"] = orgID
	if err := s.ssoSentinel.UpdateConnection(c.Param("connID"), body); err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	c.Status(http.StatusOK)
}

func (s *Service) deleteSSOConnection(c *gin.Context) {
	orgID := s.ssoOrgGuard(c)
	if orgID == "" {
		return
	}
	if err := s.ssoSentinel.DeleteConnection(c.Param("connID"), orgID); err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	c.Status(http.StatusOK)
}

func (s *Service) listSSODomains(c *gin.Context) {
	if s.ssoOrgGuard(c) == "" {
		return
	}
	data, err := s.ssoSentinel.ListDomains(c.Param("connID"))
	raw(c, data, err)
}

func (s *Service) addSSODomain(c *gin.Context) {
	if s.ssoOrgGuard(c) == "" {
		return
	}
	var body map[string]interface{}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	data, err := s.ssoSentinel.AddDomain(c.Param("connID"), body)
	raw(c, data, err)
}

func (s *Service) verifySSODomain(c *gin.Context) {
	if s.ssoOrgGuard(c) == "" {
		return
	}
	data, err := s.ssoSentinel.VerifyDomain(c.Param("connID"), c.Param("domainID"))
	raw(c, data, err)
}

func (s *Service) deleteSSODomain(c *gin.Context) {
	if s.ssoOrgGuard(c) == "" {
		return
	}
	if err := s.ssoSentinel.DeleteDomain(c.Param("connID"), c.Param("domainID")); err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	c.Status(http.StatusOK)
}
