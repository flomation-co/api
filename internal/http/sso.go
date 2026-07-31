package http

import (
	"encoding/json"
	"net/http"

	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ssoOrgGuard enforces that the caller is an admin/OrganisationManage of the org
// named in the route AND that it's their active org, then returns the org id.
// Returns "" (and aborts) when not allowed.
func (s *Service) ssoOrgGuard(c *gin.Context) string {
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
