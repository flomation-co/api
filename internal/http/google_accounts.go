package http

import (
	"fmt"
	"net/http"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// upsertGoogleAccountInternal handles POST /api/v1/internal/agent-user/:id/google-account.
// Called by Launch's OAuth callback to store the encrypted refresh token.
func (s *Service) upsertGoogleAccountInternal(c *gin.Context) {
	agentUserID := c.Param("id")

	var body struct {
		GoogleEmail  string `json:"google_email" binding:"required"`
		RefreshToken string `json:"refresh_token" binding:"required"`
		Label        string `json:"label"`
		Purpose      string `json:"purpose"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	purpose := body.Purpose
	if purpose == "" {
		purpose = "calendar"
	}

	if err := s.persistence.UpsertGoogleAccount(agentUserID, body.GoogleEmail, body.RefreshToken, body.Label, purpose); err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_user_id": agentUserID,
			"email":         body.GoogleEmail,
		}).Error("unable to store Google account")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"status": "connected", "email": body.GoogleEmail})
}

// getGoogleTokensInternal handles GET /api/v1/internal/agent-user/:id/google-tokens.
// Proxies to Launch's /internal/google/tokens/:agent_user_id endpoint which
// fetches the decrypted refresh tokens from the API's raw endpoint, exchanges
// each for a short-lived access token using Launch's Google client credentials,
// and returns the access tokens. The executor/runner never sees refresh tokens
// or client secrets — only ephemeral access tokens.
func (s *Service) getGoogleTokensInternal(c *gin.Context) {
	agentUserID := c.Param("id")

	launchURL := s.config.InternalLaunchURL()
	if launchURL == "" {
		log.Error("Launch URL not configured — cannot proxy Google token request")
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	// Pass purpose filter through to Launch
	purpose := c.Query("purpose")
	endpoint := fmt.Sprintf("%s/internal/google/tokens/%s", launchURL, agentUserID)
	if purpose != "" {
		endpoint += "?purpose=" + purpose
	}
	resp, err := s.launch.Client().Get(endpoint) // #nosec G107 — internal service-to-service call
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_user_id": agentUserID,
		}).Error("unable to proxy Google token request to Launch")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Stream the response from Launch back to the caller
	c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

// getGoogleRefreshTokensInternal handles GET /api/v1/internal/agent-user/:id/google-refresh-tokens.
// Returns raw decrypted refresh tokens. Called ONLY by Launch (service-to-service)
// for the actual token exchange. Not exposed to runners or executors.
func (s *Service) getGoogleRefreshTokensInternal(c *gin.Context) {
	agentUserID := c.Param("id")
	purpose := c.Query("purpose")

	var accounts []*api.AgentUserGoogleAccount
	var err error
	if purpose != "" {
		accounts, err = s.persistence.GetGoogleAccounts(agentUserID, purpose)
	} else {
		accounts, err = s.persistence.GetGoogleAccounts(agentUserID)
	}
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_user_id": agentUserID,
		}).Error("unable to fetch Google accounts")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	type accountResponse struct {
		Email        string `json:"email"`
		Label        string `json:"label,omitempty"`
		RefreshToken string `json:"refresh_token"`
	}

	var responses []accountResponse
	for _, acct := range accounts {
		label := ""
		if acct.Label != nil {
			label = *acct.Label
		}
		responses = append(responses, accountResponse{
			Email:        acct.GoogleEmail,
			Label:        label,
			RefreshToken: string(acct.RefreshToken),
		})
	}

	c.JSON(http.StatusOK, responses)
}

// getGoogleAccountsInternal handles GET /api/v1/internal/agent-user/:id/google-accounts.
// Returns connected accounts grouped by purpose with OAuth links for each
// purpose type. Called by the executor's google_accounts tool action.
func (s *Service) getGoogleAccountsInternal(c *gin.Context) {
	agentUserID := c.Param("id")
	agentID := c.Query("agent_id")

	accounts, err := s.persistence.GetGoogleAccounts(agentUserID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":         err,
			"agent_user_id": agentUserID,
		}).Error("unable to fetch Google accounts")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	type accountInfo struct {
		Email   string `json:"email"`
		Label   string `json:"label,omitempty"`
		Purpose string `json:"purpose"`
	}

	var accts []accountInfo
	for _, acct := range accounts {
		label := ""
		if acct.Label != nil {
			label = *acct.Label
		}
		accts = append(accts, accountInfo{
			Email:   acct.GoogleEmail,
			Label:   label,
			Purpose: acct.Purpose,
		})
	}

	// Build OAuth URLs for each purpose type
	authURLs := make(map[string]string)
	if s.config.Launch.PublicURL != "" && agentID != "" {
		for _, purpose := range []string{"calendar", "email_read", "email_send"} {
			authURLs[purpose] = fmt.Sprintf("%s/auth/google/%s?agent_id=%s&purpose=%s",
				s.config.Launch.PublicURL, agentUserID, agentID, purpose)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"accounts":  accts,
		"auth_urls": authURLs,
	})
}

// deleteGoogleAccountInternal handles DELETE /api/v1/internal/agent-user/:id/google-account/:email.
// Optional ?purpose= query param to delete only a specific purpose (otherwise deletes all).
func (s *Service) deleteGoogleAccountInternal(c *gin.Context) {
	agentUserID := c.Param("id")
	email := c.Param("email")
	purpose := c.Query("purpose")

	var err error
	if purpose != "" {
		err = s.persistence.DeleteGoogleAccount(agentUserID, email, purpose)
	} else {
		err = s.persistence.DeleteGoogleAccount(agentUserID, email)
	}
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Status(http.StatusNoContent)
}
