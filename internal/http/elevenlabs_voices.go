package http

import (
	"encoding/json"
	"fmt"
	"io"
	gohttp "net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// getElevenLabsVoices handles GET /api/v1/environment/:environment/elevenlabs-voices/:credential
// Resolves the ElevenLabs API key from the environment secrets, then proxies
// a request to the ElevenLabs voices endpoint.
func (s *Service) getElevenLabsVoices(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(gohttp.StatusUnauthorized)
		return
	}

	environmentID := c.Param("environment")
	credentialRef := c.Param("credential")

	// The credential comes URL-encoded as "${secrets.ELEVENLABS_API_KEY}" or similar.
	// Strip the ${secrets.} wrapper to get the secret name.
	secretName := credentialRef
	if strings.HasPrefix(secretName, "${secrets.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[10 : len(secretName)-1]
	} else if strings.HasPrefix(secretName, "${secret.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[9 : len(secretName)-1]
	}

	if secretName == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Invalid credential reference"})
		return
	}

	// Look up the environment
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Environment not found"})
		return
	}

	// Find the secret by name
	secret, err := s.persistence.GetEnvironmentSecretByName(environmentID, env.SecretKey, secretName)
	if err != nil || secret == nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("Secret '%s' not found in environment", secretName)})
		return
	}

	apiKey := secret.Value
	if apiKey == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Secret value is empty"})
		return
	}

	// Call ElevenLabs voices API
	req, err := gohttp.NewRequest(gohttp.MethodGet, "https://api.elevenlabs.io/v1/voices", nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to create request"})
		return
	}
	req.Header.Set("xi-api-key", apiKey)

	client := &gohttp.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Warn("failed to fetch ElevenLabs voices")
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to connect to ElevenLabs API"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != gohttp.StatusOK {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("ElevenLabs returned %d", resp.StatusCode)})
		return
	}

	var result json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse ElevenLabs response"})
		return
	}

	c.Data(gohttp.StatusOK, "application/json", body)
}

// getElevenLabsModels handles GET /api/v1/environment/:environment/elevenlabs-models/:credential
// Same pattern as voices — resolves API key from secrets, proxies to ElevenLabs models endpoint.
func (s *Service) getElevenLabsModels(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(gohttp.StatusUnauthorized)
		return
	}

	environmentID := c.Param("environment")
	credentialRef := c.Param("credential")

	secretName := credentialRef
	if strings.HasPrefix(secretName, "${secrets.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[10 : len(secretName)-1]
	} else if strings.HasPrefix(secretName, "${secret.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[9 : len(secretName)-1]
	}

	if secretName == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Invalid credential reference"})
		return
	}

	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Environment not found"})
		return
	}

	secret, err := s.persistence.GetEnvironmentSecretByName(environmentID, env.SecretKey, secretName)
	if err != nil || secret == nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("Secret '%s' not found", secretName)})
		return
	}

	apiKey := secret.Value
	if apiKey == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Secret value is empty"})
		return
	}

	req, _ := gohttp.NewRequest(gohttp.MethodGet, "https://api.elevenlabs.io/v1/models", nil)
	req.Header.Set("xi-api-key", apiKey)

	client := &gohttp.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to connect to ElevenLabs API"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != gohttp.StatusOK {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("ElevenLabs returned %d", resp.StatusCode)})
		return
	}

	c.Data(gohttp.StatusOK, "application/json", body)
}
