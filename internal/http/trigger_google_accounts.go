package http

import (
	"fmt"
	"net/http"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// upsertTriggerGoogleAccountInternal handles POST /api/v1/internal/trigger/:id/google-account.
// Called by Launch's OAuth callback to store an encrypted refresh token
// scoped to a trigger.
func (s *Service) upsertTriggerGoogleAccountInternal(c *gin.Context) {
	triggerID := c.Param("id")

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
		purpose = "email_read"
	}

	if err := s.persistence.UpsertTriggerGoogleAccount(triggerID, body.GoogleEmail, body.RefreshToken, body.Label, purpose); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": triggerID,
			"email":      body.GoogleEmail,
		}).Error("unable to store trigger Google account")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"status": "connected", "email": body.GoogleEmail, "purpose": purpose})
}

// getTriggerGoogleAccountsInternal handles GET /api/v1/internal/trigger/:id/google-accounts.
// Returns connected accounts for a trigger (email + label + purpose, no tokens).
func (s *Service) getTriggerGoogleAccountsInternal(c *gin.Context) {
	triggerID := c.Param("id")

	accounts, err := s.persistence.GetTriggerGoogleAccounts(triggerID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": triggerID,
		}).Error("unable to fetch trigger Google accounts")
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

	// Build OAuth URLs for connecting accounts to this trigger
	authURLs := make(map[string]string)
	if s.config.Launch.PublicURL != "" {
		for _, purpose := range []string{"email_read", "email_send"} {
			authURLs[purpose] = fmt.Sprintf("%s/auth/google/trigger/%s?purpose=%s",
				s.config.Launch.PublicURL, triggerID, purpose)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"accounts":  accts,
		"auth_urls": authURLs,
	})
}

// getTriggerGoogleTokensInternal handles GET /api/v1/internal/trigger/:id/google-tokens.
// Proxies to Launch for token refresh — returns access tokens, not refresh tokens.
func (s *Service) getTriggerGoogleTokensInternal(c *gin.Context) {
	triggerID := c.Param("id")

	launchURL := s.config.Launch.URL
	if launchURL == "" {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	endpoint := fmt.Sprintf("%s/internal/google/tokens/trigger/%s", launchURL, triggerID)
	if purpose := c.Query("purpose"); purpose != "" {
		endpoint += "?purpose=" + purpose
	}
	resp, err := http.Get(endpoint) // #nosec G107 — internal service-to-service call
	if err != nil {
		log.WithFields(log.Fields{"error": err, "trigger_id": triggerID}).Error("unable to proxy trigger Google token request")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

// getTriggerGoogleRefreshTokensInternal handles GET /api/v1/internal/trigger/:id/google-refresh-tokens.
// Returns raw decrypted refresh tokens for a trigger's connected accounts.
// Called ONLY by Launch for token exchange.
func (s *Service) getTriggerGoogleRefreshTokensInternal(c *gin.Context) {
	triggerID := c.Param("id")
	purpose := c.Query("purpose")

	var accounts []*api.TriggerGoogleAccount
	var err error
	if purpose != "" {
		accounts, err = s.persistence.GetTriggerGoogleAccounts(triggerID, purpose)
	} else {
		accounts, err = s.persistence.GetTriggerGoogleAccounts(triggerID)
	}
	if err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": triggerID,
		}).Error("unable to fetch trigger Google accounts")
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

// deleteTriggerGoogleAccountInternal handles DELETE /api/v1/internal/trigger/:id/google-account/:email.
func (s *Service) deleteTriggerGoogleAccountInternal(c *gin.Context) {
	triggerID := c.Param("id")
	email := c.Param("email")
	purpose := c.Query("purpose")

	var err error
	if purpose != "" {
		err = s.persistence.DeleteTriggerGoogleAccount(triggerID, email, purpose)
	} else {
		err = s.persistence.DeleteTriggerGoogleAccount(triggerID, email)
	}
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Status(http.StatusNoContent)
}