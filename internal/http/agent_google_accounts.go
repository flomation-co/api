package http

import (
	"github.com/gin-gonic/gin"
)

// getAgentGoogleAccounts handles GET /api/v1/agent/:id/google-accounts.
// JWT-auth'd endpoint for the editor to list connected Google accounts.
// Delegates to the trigger-scoped handler (agent ID is used as trigger scope key).
func (s *Service) getAgentGoogleAccounts(c *gin.Context) {
	s.getTriggerGoogleAccountsInternal(c)
}

// deleteAgentGoogleAccount handles DELETE /api/v1/agent/:id/google-account/:email.
// JWT-auth'd endpoint for the editor to remove a connected Google account.
func (s *Service) deleteAgentGoogleAccount(c *gin.Context) {
	s.deleteTriggerGoogleAccountInternal(c)
}
