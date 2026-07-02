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

// resolveEnvironmentSecret is the secret-lookup boilerplate shared by the
// editor option-fetch proxies (this file's ElevenLabs endpoints, the
// ollama-models resolver). Centralising it keeps the secret extraction and
// the various error-shape choices consistent across endpoints; small
// new endpoints only have to call this and then make the upstream request.
//
// Returns (apiKey, "") on success or ("", errorMessage) on failure.
// The caller is responsible for writing the JSON error response — this
// helper deliberately doesn't touch the gin.Context so unit testing the
// resolution logic stays easy.
func (s *Service) resolveEnvironmentSecret(c *gin.Context, environmentID, credentialRef string) (string, string) {
	user := s.getUserFromContext(c)
	if user == nil {
		return "", "unauthorized"
	}
	secretName := credentialRef
	if strings.HasPrefix(secretName, "${secrets.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[10 : len(secretName)-1]
	} else if strings.HasPrefix(secretName, "${secret.") && strings.HasSuffix(secretName, "}") {
		secretName = secretName[9 : len(secretName)-1]
	}
	if secretName == "" {
		return "", "Invalid credential reference"
	}
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		return "", "Environment not found"
	}
	secret, err := s.persistence.GetEnvironmentSecretByName(environmentID, env.SecretKey, secretName)
	if err != nil || secret == nil {
		return "", fmt.Sprintf("Secret '%s' not found in environment", secretName)
	}
	apiKey := secret.Value
	if apiKey == "" {
		return "", "Secret value is empty"
	}
	return apiKey, ""
}

// getElevenLabsSharedVoices handles GET /api/v1/environment/:environment/elevenlabs-shared-voices/:credential
// The public Voice Library proxy. Passes through search, page_size, page
// query parameters so the editor can implement search + paginated browse
// without each request hitting our DB. All other ElevenLabs filter
// parameters (gender, accent, language, sort) are also forwarded as-is.
func (s *Service) getElevenLabsSharedVoices(c *gin.Context) {
	environmentID := c.Param("environment")
	credentialRef := c.Param("credential")

	apiKey, errMsg := s.resolveEnvironmentSecret(c, environmentID, credentialRef)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	upstream := "https://api.elevenlabs.io/v1/shared-voices"
	if raw := c.Request.URL.RawQuery; raw != "" {
		upstream += "?" + raw
	}

	req, err := gohttp.NewRequest(gohttp.MethodGet, upstream, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to create request"})
		return
	}
	req.Header.Set("xi-api-key", apiKey)

	client := &gohttp.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Warn("failed to fetch ElevenLabs shared voices")
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to connect to ElevenLabs API"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != gohttp.StatusOK {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("ElevenLabs returned %d", resp.StatusCode)})
		return
	}
	c.Data(gohttp.StatusOK, "application/json", body)
}

// addElevenLabsVoiceBody is the payload from the editor when the user
// hits "+ Add to Library" on a shared voice. We require all three
// fields explicitly — public_user_id distinguishes which cloner's
// voice this is (the same voice_id can appear under different cloners
// historically), and new_name is what the voice will be called in the
// user's own library.
type addElevenLabsVoiceBody struct {
	PublicUserID string `json:"public_user_id"`
	VoiceID      string `json:"voice_id"`
	NewName      string `json:"new_name"`
}

// addElevenLabsVoice handles POST /api/v1/environment/:environment/elevenlabs-add-voice/:credential
// Adds a shared voice to the user's personal library. After this
// succeeds, the next call to /elevenlabs-voices/ includes the new
// voice and it's usable as a voice_id in TTS actions.
func (s *Service) addElevenLabsVoice(c *gin.Context) {
	environmentID := c.Param("environment")
	credentialRef := c.Param("credential")

	apiKey, errMsg := s.resolveEnvironmentSecret(c, environmentID, credentialRef)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	var body addElevenLabsVoiceBody
	if err := c.ShouldBindJSON(&body); err != nil || body.PublicUserID == "" || body.VoiceID == "" || body.NewName == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "public_user_id, voice_id and new_name are all required"})
		return
	}

	upstream := fmt.Sprintf("https://api.elevenlabs.io/v1/voices/add/%s/%s", body.PublicUserID, body.VoiceID)
	payload, _ := json.Marshal(map[string]string{"new_name": body.NewName})

	req, err := gohttp.NewRequest(gohttp.MethodPost, upstream, strings.NewReader(string(payload)))
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to create request"})
		return
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &gohttp.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Warn("failed to add ElevenLabs shared voice")
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to connect to ElevenLabs API"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != gohttp.StatusOK {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("ElevenLabs returned %d: %s", resp.StatusCode, string(respBody))})
		return
	}
	c.Data(gohttp.StatusOK, "application/json", respBody)
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
