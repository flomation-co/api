package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// getFacebookPages handles GET /api/v1/environment/:environment/facebook-pages/:credentialName.
// Resolves the OAuth credential, calls Facebook's /me/accounts, and returns
// the list of pages the user manages. Handles appsecret_proof server-side.
func (s *Service) getFacebookPages(c *gin.Context) {
	envID := c.Param("environment")
	credentialName := c.Param("credentialName")

	env, err := s.persistence.GetEnvironmentByIDDirect(envID)
	if err != nil || env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Resolve the access token
	accessToken, err := s.persistence.GetCredentialByName(envID, credentialName, env.SecretKey)
	if err != nil || accessToken == nil || *accessToken == "" {
		c.JSON(http.StatusOK, gin.H{"pages": []interface{}{}, "error": "Credential not found or has no access token"})
		return
	}

	// Look up app_secret from environment secrets (optional)
	appSecret := ""
	sec, err := s.persistence.GetEnvironmentSecretByName(envID, env.SecretKey, "facebook_app_secret")
	if err == nil && sec != nil {
		appSecret = sec.Value
	}
	// Also try FACEBOOK_APP_SECRET
	if appSecret == "" {
		sec, err = s.persistence.GetEnvironmentSecretByName(envID, env.SecretKey, "FACEBOOK_APP_SECRET")
		if err == nil && sec != nil {
			appSecret = sec.Value
		}
	}

	// Build Graph API URL
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/me/accounts?access_token=%s&fields=id,name,category", *accessToken)
	if appSecret != "" {
		mac := hmac.New(sha256.New, []byte(appSecret))
		mac.Write([]byte(*accessToken))
		apiURL += "&appsecret_proof=" + hex.EncodeToString(mac.Sum(nil))
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(apiURL) // #nosec G107
	if err != nil {
		log.WithError(err).Error("failed to call Facebook Graph API")
		c.JSON(http.StatusOK, gin.H{"pages": []interface{}{}, "error": "Failed to connect to Facebook"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		c.JSON(http.StatusOK, gin.H{"pages": []interface{}{}, "error": errResp.Error.Message})
		return
	}

	var result struct {
		Data []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		c.JSON(http.StatusOK, gin.H{"pages": []interface{}{}, "error": "Failed to parse response"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"pages": result.Data})
}
