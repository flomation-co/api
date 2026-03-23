package http

import (
	"net/http"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// checkPermission verifies that the current user has the required permission
// within their active organisation. Returns true if access is granted.
// In personal mode (no organisation), all permissions are granted.
// Organisation admins implicitly have all permissions.
// When no RBAC groups are configured, members get a default permission set.
func (s *Service) checkPermission(c *gin.Context, required rbac.Permission) bool {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return false
	}

	// Personal mode — no RBAC applies
	if len(user.Organisations) == 0 {
		return true
	}

	orgID := user.Organisations[0].ID

	// Check if user is an admin — admins have implicit full access
	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to check user role")
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}

	if role != nil && *role == "admin" {
		return true
	}

	perms := s.getEffectivePermissions(orgID, user.ID)

	if !rbac.HasPermission(perms, required) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":    "insufficient_permissions",
			"required": string(required),
		})
		return false
	}

	return true
}

// getEffectivePermissions returns the user's effective permissions in the org.
// If the user has no group memberships, returns the default member permissions.
func (s *Service) getEffectivePermissions(orgID, userID string) []string {
	count, err := s.persistence.CountUserGroupsInOrganisation(orgID, userID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to count user groups")
		return nil
	}

	// No groups configured for this user — grant defaults
	if count == 0 {
		return rbac.DefaultMemberPermissions
	}

	perms, err := s.persistence.GetUserPermissionsInOrganisation(orgID, userID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get user permissions")
		return nil
	}

	return perms
}

// getMyPermissions returns the current user's effective permissions for the active org.
func (s *Service) getMyPermissions(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		// Personal mode — all permissions
		c.JSON(http.StatusOK, api.UserPermissions{
			Role:        "personal",
			Permissions: rbac.AllPermissions(),
			IsAdmin:     true,
		})
		return
	}

	orgID := user.Organisations[0].ID

	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if *role == "admin" {
		c.JSON(http.StatusOK, api.UserPermissions{
			Role:        "admin",
			Permissions: rbac.AllPermissions(),
			IsAdmin:     true,
		})
		return
	}

	perms := s.getEffectivePermissions(orgID, user.ID)

	c.JSON(http.StatusOK, api.UserPermissions{
		Role:        *role,
		Permissions: perms,
		IsAdmin:     false,
	})
}
