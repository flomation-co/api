package http

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	api "flomation.app/automate/api"
)

// ── Credential providers ────────────────────────────────────────────

type providerResponse struct {
	api.CredentialProvider
	Configured bool `json:"configured"`
}

func (s *Service) getCredentialProviders(c *gin.Context) {
	providers, err := s.persistence.GetCredentialProviders()
	if err != nil {
		log.WithError(err).Error("unable to list credential providers")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	var result []providerResponse
	for _, p := range providers {
		configured := false
		if s.config.OAuth != nil {
			if oa, ok := s.config.OAuth[p.Slug]; ok && oa.ClientID != "" {
				configured = true
			}
		}
		result = append(result, providerResponse{
			CredentialProvider: p,
			Configured:         configured,
		})
	}

	c.JSON(http.StatusOK, result)
}

// ── Environment credentials CRUD ────────────────────────────────────

func (s *Service) getEnvironmentCredentials(c *gin.Context) {
	environmentID := c.Param("environment")

	creds, err := s.persistence.GetCredentialsByEnvironmentID(environmentID)
	if err != nil {
		log.WithError(err).Error("unable to list credentials")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, creds)
}

func (s *Service) deleteEnvironmentCredential(c *gin.Context) {
	environmentID := c.Param("environment")
	credID := c.Param("id")

	if err := s.persistence.DeleteCredential(credID, environmentID); err != nil {
		log.WithError(err).Error("unable to delete credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

// ── OAuth initiation ────────────────────────────────────────────────

type createCredentialRequest struct {
	ProviderSlug string  `json:"provider_slug" binding:"required"`
	Name         string  `json:"name" binding:"required"`
	Scopes       *string `json:"scopes"`
	ClientID     *string `json:"client_id"`
	ClientSecret *string `json:"client_secret"`
	// URLVars supplies per-tenant OAuth URL variable values (e.g.
	// {"shop":"my-store"}) for providers that declare url_variables.
	URLVars map[string]string `json:"url_vars"`
	// RoleARN / Region apply only to the aws_role provider: the customer's IAM
	// role to assume, and its region.
	RoleARN string `json:"role_arn"`
	Region  string `json:"region"`
}

func (s *Service) createEnvironmentCredential(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)

	var req createCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Validate provider exists
	provider, err := s.persistence.GetCredentialProvider(req.ProviderSlug)
	if err != nil || provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown provider"})
		return
	}

	// Get environment for encryption key
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "environment not found"})
		return
	}

	// aws_role is a token-less credential: no OAuth round-trip. Generate an
	// External ID, store the role details in metadata, and return the trust
	// policy for the customer to paste into their AWS role.
	if req.ProviderSlug == "aws_role" {
		s.createAWSRoleCredential(c, environmentID, req)
		return
	}

	// Use provider defaults if client credentials not provided
	scopes := provider.DefaultScopes
	if req.Scopes != nil && *req.Scopes != "" {
		scopes = req.Scopes
	}

	// Validate the provider's per-tenant URL variables are all supplied and
	// host-safe (surfaces a clear message before an OAuth round-trip).
	for _, v := range provider.URLVariables() {
		val := req.URLVars[v.Key]
		if val == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is required", v.Label)})
			return
		}
	}
	if _, err := api.SubstituteURLVariables(provider.AuthURL, req.URLVars); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	metadata, err := api.MetadataWithURLVars(req.URLVars)
	if err != nil {
		log.WithError(err).Error("unable to encode credential metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	cred := &api.EnvironmentCredential{
		EnvironmentID: environmentID,
		ProviderSlug:  req.ProviderSlug,
		Name:          req.Name,
		Scopes:        scopes,
		ClientID:      req.ClientID,
		ClientSecret:  req.ClientSecret,
		Metadata:      metadata,
	}

	credID, err := s.persistence.CreateCredential(cred, env.SecretKey)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists in this environment"})
			return
		}
		log.WithError(err).Error("unable to create credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Build the OAuth authorization URL
	authURL, err := s.buildOAuthURL(credID, environmentID, provider, env.SecretKey, req.ClientID, scopes, req.URLVars)
	if err != nil {
		log.WithError(err).Error("unable to build OAuth URL")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":       credID,
		"auth_url": authURL,
	})
}

// ── Re-authorise existing credential ────────────────────────────────

func (s *Service) reauthoriseCredential(c *gin.Context) {
	environmentID := c.Param("environment")
	credID := c.Param("id")
	user := s.getUserFromContext(c)

	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil || cred == nil || cred.EnvironmentID != environmentID {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}

	provider, err := s.persistence.GetCredentialProvider(cred.ProviderSlug)
	if err != nil || provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown provider"})
		return
	}

	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "environment not found"})
		return
	}

	// Get client credentials from the stored credential (if custom)
	clientID, _, _ := s.persistence.GetDecryptedClientCredentials(credID, env.SecretKey)

	// Re-validate the stored URL variables against the provider's current
	// declaration before rebuilding the OAuth URL: a credential created before
	// the provider gained a required variable would otherwise fail later with a
	// generic substitution error. Surface a clear, actionable message instead.
	urlVars := api.URLVarsFromMetadata(cred.Metadata)
	if err := provider.ValidateURLVars(urlVars); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	authURL, err := s.buildOAuthURL(credID, environmentID, provider, env.SecretKey, clientID, cred.Scopes, urlVars)
	if err != nil {
		log.WithError(err).Error("unable to build OAuth URL")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Reset status to pending
	_ = s.persistence.UpdateCredentialStatus(credID, api.CredentialStatusPending, nil)

	c.JSON(http.StatusOK, gin.H{"auth_url": authURL})
}

// ── OAuth callback ──────────────────────────────────────────────────

func (s *Service) credentialOAuthCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")
	errParam := c.Query("error")

	if errParam != "" {
		errDesc := c.Query("error_description")
		log.WithFields(log.Fields{"error": errParam, "description": errDesc}).Warn("OAuth callback error")
		c.Data(http.StatusOK, "text/html", []byte(oauthResultPage("Authorisation Failed", errDesc, true)))
		return
	}

	if code == "" || state == "" {
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Invalid Request", "Missing code or state parameter.", true)))
		return
	}

	// Decode state
	stateJSON, err := base64.URLEncoding.DecodeString(state)
	if err != nil {
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Invalid State", "Could not decode state.", true)))
		return
	}

	var stateData struct {
		CredentialID  string `json:"cid"`
		EnvironmentID string `json:"eid"`
	}
	if err := json.Unmarshal(stateJSON, &stateData); err != nil {
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Invalid State", "Could not parse state.", true)))
		return
	}

	// Look up credential and environment
	cred, err := s.persistence.GetCredentialByID(stateData.CredentialID)
	if err != nil {
		// Distinguish "actually missing" from "lookup errored" so
		// the log surfaces schema/scan failures (missing struct
		// field, migration-without-struct-update, etc.) rather
		// than pretending they're routine "user's row is gone".
		// The user-facing message stays the same to avoid leaking
		// internal detail.
		log.WithFields(log.Fields{
			"credential_id": stateData.CredentialID,
			"error":         err,
		}).Error("credential lookup failed during OAuth callback")
		c.Data(http.StatusNotFound, "text/html", []byte(oauthResultPage("Not Found", "Credential not found.", true)))
		return
	}
	if cred == nil {
		log.WithFields(log.Fields{
			"credential_id": stateData.CredentialID,
		}).Warn("credential row not found during OAuth callback")
		c.Data(http.StatusNotFound, "text/html", []byte(oauthResultPage("Not Found", "Credential not found.", true)))
		return
	}

	provider, err := s.persistence.GetCredentialProvider(cred.ProviderSlug)
	if err != nil || provider == nil {
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Error", "Unknown provider.", true)))
		return
	}

	// Get environment key for decryption/encryption
	env, err := s.persistence.GetEnvironmentByIDDirect(stateData.EnvironmentID)
	if err != nil || env == nil {
		c.Data(http.StatusNotFound, "text/html", []byte(oauthResultPage("Error", "Environment not found.", true)))
		return
	}

	// Get client credentials
	clientID, clientSecret, err := s.persistence.GetDecryptedClientCredentials(stateData.CredentialID, env.SecretKey)
	if err != nil {
		log.WithError(err).Error("unable to decrypt client credentials")
	}

	// Use provider defaults from config if no custom credentials stored
	if clientID == nil || *clientID == "" {
		clientID, clientSecret = s.getDefaultClientCredentials(cred.ProviderSlug)
	}

	if clientID == nil || clientSecret == nil {
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Configuration Error", "No client credentials configured for this provider.", true)))
		return
	}

	// Substitute per-tenant URL variables (stored on the credential) into the
	// token URL, mirroring the authorize URL.
	tokenURL, err := api.SubstituteURLVariables(provider.TokenURL, api.URLVarsFromMetadata(cred.Metadata))
	if err != nil {
		log.WithError(err).Error("unable to build token URL")
		c.Data(http.StatusBadRequest, "text/html", []byte(oauthResultPage("Configuration Error", err.Error(), true)))
		return
	}

	// Exchange code for tokens
	callbackURL := s.credentialCallbackURL()
	tokenResp, err := exchangeOAuthCode(tokenURL, code, *clientID, *clientSecret, callbackURL, cred.ProviderSlug)
	if err != nil {
		log.WithError(err).Error("OAuth token exchange failed")
		errMsg := err.Error()
		_ = s.persistence.UpdateCredentialStatus(stateData.CredentialID, api.CredentialStatusError, &errMsg)
		c.Data(http.StatusOK, "text/html", []byte(oauthResultPage("Token Exchange Failed", err.Error(), true)))
		return
	}

	// Calculate expiry
	var expiresAt *time.Time
	if tokenResp.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	// Store tokens (also persist client credentials so the refresh poller can use them)
	if err := s.persistence.StoreCredentialTokens(
		stateData.CredentialID, env.SecretKey,
		tokenResp.AccessToken, tokenResp.RefreshToken,
		*clientID, *clientSecret, expiresAt,
	); err != nil {
		log.WithError(err).Error("unable to store credential tokens")
		c.Data(http.StatusOK, "text/html", []byte(oauthResultPage("Storage Error", "Failed to save tokens.", true)))
		return
	}

	// Capture the per-account identifier that's only knowable after auth
	// (QuickBooks realmId / Xero tenantId) into the credential metadata. No-op
	// for every other provider. Non-fatal — see captureProviderTenant.
	s.captureProviderTenant(c, stateData.CredentialID, cred.ProviderSlug, cred.Metadata, tokenResp.AccessToken)

	log.WithFields(log.Fields{
		"credential_id": stateData.CredentialID,
		"provider":      cred.ProviderSlug,
		"has_refresh":   tokenResp.RefreshToken != "",
	}).Info("credential authorised successfully")

	c.Data(http.StatusOK, "text/html", []byte(oauthResultPage("Authorised Successfully", "You can close this window and return to Flomation.", false)))
}

// ── Helpers ─────────────────────────────────────────────────────────

func (s *Service) buildOAuthURL(credID, envID string, provider *api.CredentialProvider, envKey string, clientID *string, scopes *string, urlVars map[string]string) (string, error) {
	// Get client ID (custom or default)
	cID := clientID
	if cID == nil || *cID == "" {
		cID, _ = s.getDefaultClientCredentials(provider.Slug)
	}
	if cID == nil || *cID == "" {
		return "", fmt.Errorf("no client ID configured for provider %s", provider.Slug)
	}

	// Substitute per-tenant URL variables (e.g. the shop subdomain) into the
	// provider's authorize URL. A fixed-URL provider is unaffected.
	authURL, err := api.SubstituteURLVariables(provider.AuthURL, urlVars)
	if err != nil {
		return "", err
	}

	// Build state parameter
	stateJSON, _ := json.Marshal(struct {
		CredentialID  string `json:"cid"`
		EnvironmentID string `json:"eid"`
	}{credID, envID})
	state := base64.URLEncoding.EncodeToString(stateJSON)

	// Build URL
	params := url.Values{
		"client_id":     {*cID},
		"redirect_uri":  {s.credentialCallbackURL()},
		"response_type": {"code"},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	if scopes != nil && *scopes != "" {
		params.Set("scope", *scopes)
	}

	// Twitter uses PKCE
	if provider.Slug == "twitter" {
		verifier := generateCodeVerifier()
		params.Set("code_challenge", verifier)
		params.Set("code_challenge_method", "plain")
	}

	return authURL + "?" + params.Encode(), nil
}

func (s *Service) credentialCallbackURL() string {
	baseURL := s.config.Launch.APIURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", s.config.HttpListenConfig.Port)
	}
	return strings.TrimRight(baseURL, "/") + "/api/v1/credential/callback"
}

func (s *Service) getDefaultClientCredentials(providerSlug string) (*string, *string) {
	if s.config.OAuth == nil {
		return nil, nil
	}
	prov, ok := s.config.OAuth[providerSlug]
	if !ok || prov.ClientID == "" {
		return nil, nil
	}
	return &prov.ClientID, &prov.ClientSecret
}

// providerIsSandbox reports whether the platform-configured app for a provider
// points at a sandbox/test environment (see OAuthProviderConfig.Sandbox). Used
// at OAuth connect time to stamp the environment onto the credential so the
// executor can pick the right API host without a user-facing toggle.
func (s *Service) providerIsSandbox(providerSlug string) bool {
	if s.config.OAuth == nil {
		return false
	}
	return s.config.OAuth[providerSlug].Sandbox
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func exchangeOAuthCode(tokenURL, code, clientID, clientSecret, redirectURI, providerSlug string) (*oauthTokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}

	// Intuit (and Xero) require the client credentials via HTTP Basic auth and
	// reject them in the body; every other provider takes them in the body.
	basicAuth := api.ProviderUsesBasicAuth(providerSlug)
	if !basicAuth {
		data.Set("client_id", clientID)
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basicAuth {
		req.SetBasicAuth(clientID, clientSecret)
	}

	// GitHub requires Accept: application/json explicitly
	if providerSlug == "github" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	return &tokenResp, nil
}

func generateCodeVerifier() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func oauthResultPage(title, message string, isError bool) string {
	colour := "#00aa9c"
	if isError {
		colour = "#f87171"
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>%s - Flomation</title>
<style>
  body { font-family: -apple-system, sans-serif; background: #161019; color: #fff;
         display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
  .card { text-align: center; max-width: 400px; padding: 40px; }
  h1 { font-size: 20px; color: %s; margin-bottom: 8px; }
  p { font-size: 14px; color: rgba(255,255,255,0.6); line-height: 1.5; }
</style>
</head>
<body><div class="card"><h1>%s</h1><p>%s</p></div></body>
</html>`, title, colour, title, message)
}
