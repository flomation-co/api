package http

import (
	"encoding/json"
	"fmt"
	"io"
	gohttp "net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// This file backs the live "Model" dropdowns on the AI provider actions
// (ai/anthropic, ai/openai, ai/gemini, ai/groq, ai/openwebui). Each is a
// dynamic-options proxy: the editor forwards the node's api_key (and, for
// self-hosted Open WebUI, its endpoint) and the api fetches the provider's
// models list server-side, so a paste-a-key model can offer a live dropdown
// without the plaintext key ever transiting the browser. OpenRouter and
// Ollama have their own proxies (openrouter_models.go / ollama_models.go).
//
// Every handler follows the option-proxy convention: HTTP 200 with either
// {"options": [{name, value}]} or {"error": "..."} so the editor renders the
// message inline and falls back to the action's static options.

// aiModelsHTTPClient is shared across the fixed-host providers (Anthropic,
// OpenAI, Gemini, Groq). Open WebUI's host is caller-supplied, so it reuses
// ollamaModelsHTTPClient, whose dialer refuses link-local destinations
// (SSRF guard — see ollama_models.go).
var aiModelsHTTPClient = &gohttp.Client{Timeout: 10 * time.Second}

// Upstream model-list URLs are vars (not consts) so tests can point them at
// an httptest server — the same seam as openRouterModelsURL.
var (
	anthropicModelsURL = "https://api.anthropic.com/v1/models?limit=1000"
	openAIModelsURL    = "https://api.openai.com/v1/models"
	groqModelsURL      = "https://api.groq.com/openai/v1/models"
	geminiModelsURL    = "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000"
)

// resolveOptionAPIKey resolves an api_key query parameter for a model-list
// proxy. A plain key passes through; a ${secrets.X} reference is resolved
// server-side — gated by EnvironmentView, because the resolved value is sent
// as a credential to an upstream, so without the check a member denied
// environment.view could read any secret. A ${credentials.X} managed-
// credential reference is rejected (this resolver only handles environment
// secrets). When ok is false the response has already been written.
func (s *Service) resolveOptionAPIKey(c *gin.Context, apiKey string) (string, bool) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API key to load the model list"})
		return "", false
	}
	if strings.HasPrefix(apiKey, "${credentials.") || strings.HasPrefix(apiKey, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load the model list — use an environment secret for the API key (the flow itself still runs)"})
		return "", false
	}
	if strings.HasPrefix(apiKey, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API key secret"})
			return "", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, apiKey)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", false
		}
		return resolved, true
	}
	return apiKey, true
}

// aiModelsStatusError carries a non-2xx upstream status so handlers can map
// 401/403 to a "check the API key" hint.
type aiModelsStatusError struct {
	provider string
	status   int
}

func (e *aiModelsStatusError) Error() string {
	return fmt.Sprintf("%s models endpoint returned status %d %s", e.provider, e.status, gohttp.StatusText(e.status))
}

// unauthorised reports whether the upstream rejected the key.
func unauthorised(err error) bool {
	se, ok := err.(*aiModelsStatusError)
	return ok && (se.status == gohttp.StatusUnauthorized || se.status == gohttp.StatusForbidden)
}

// modelListOptionResponse writes the standard option-proxy response, mapping
// an auth failure to a key hint and any other error to a generic message.
func modelListOptionResponse(c *gin.Context, provider string, options []api.InputOption, err error) {
	if err != nil {
		log.WithFields(log.Fields{"provider": provider, "error": err}).Warn("unable to fetch AI models")
		if unauthorised(err) {
			c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("%s rejected the request as unauthorised — check the API key", provider)})
			return
		}
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("Could not reach %s to load the model list", provider)})
		return
	}
	if len(options) == 0 {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("%s returned no usable models", provider)})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

func sortOptions(options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
}

// === Anthropic ===

func (s *Service) getAnthropicModels(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchAnthropicModels(c, apiKey)
	modelListOptionResponse(c, "Anthropic", options, err)
}

func fetchAnthropicModels(c *gin.Context, apiKey string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, anthropicModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := aiModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &aiModelsStatusError{provider: "Anthropic", status: resp.StatusCode}
	}

	var upstream struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}
	options := make([]api.InputOption, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		options = append(options, api.InputOption{Name: name, Value: m.ID})
	}
	sortOptions(options)
	return options, nil
}

// === OpenAI ===

func (s *Service) getOpenAIModels(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchOpenAIModels(c, apiKey)
	modelListOptionResponse(c, "OpenAI", options, err)
}

func fetchOpenAIModels(c *gin.Context, apiKey string) ([]api.InputOption, error) {
	options, err := fetchOpenAICompatibleModels(c, openAIModelsURL, apiKey, "OpenAI")
	if err != nil {
		return nil, err
	}
	// OpenAI's catalogue mixes chat models with embeddings, audio, image and
	// moderation models the Chat Completions action can't use — keep only the
	// chat-capable families.
	filtered := options[:0]
	for _, o := range options {
		if isOpenAIChatModel(o.Value) {
			filtered = append(filtered, o)
		}
	}
	return filtered, nil
}

// isOpenAIChatModel keeps the GPT and o-series chat families and drops the
// non-chat model types that share the /models catalogue.
func isOpenAIChatModel(id string) bool {
	l := strings.ToLower(id)
	for _, bad := range []string{"embedding", "whisper", "tts", "audio", "realtime", "dall-e", "image", "moderation", "transcribe", "search", "codex"} {
		if strings.Contains(l, bad) {
			return false
		}
	}
	return strings.HasPrefix(l, "gpt-") ||
		strings.HasPrefix(l, "chatgpt") ||
		strings.HasPrefix(l, "o1") ||
		strings.HasPrefix(l, "o3") ||
		strings.HasPrefix(l, "o4")
}

// === Groq ===

func (s *Service) getGroqModels(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchOpenAICompatibleModels(c, groqModelsURL, apiKey, "Groq")
	modelListOptionResponse(c, "Groq", options, err)
}

// fetchOpenAICompatibleModels reads an OpenAI-shaped {data:[{id}]} model list
// with Bearer auth. Shared by OpenAI and Groq (both speak this format).
func fetchOpenAICompatibleModels(c *gin.Context, endpoint, apiKey, provider string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := aiModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &aiModelsStatusError{provider: provider, status: resp.StatusCode}
	}

	var upstream struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}
	options := make([]api.InputOption, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		if m.ID == "" {
			continue
		}
		options = append(options, api.InputOption{Name: m.ID, Value: m.ID})
	}
	sortOptions(options)
	return options, nil
}

// === Gemini ===

func (s *Service) getGeminiModels(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchGeminiModels(c, apiKey)
	modelListOptionResponse(c, "Gemini", options, err)
}

func fetchGeminiModels(c *gin.Context, apiKey string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, geminiModelsURL, nil)
	if err != nil {
		return nil, err
	}
	// Header auth keeps the key out of proxy access logs.
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := aiModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &aiModelsStatusError{provider: "Gemini", status: resp.StatusCode}
	}

	var upstream struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}
	options := make([]api.InputOption, 0, len(upstream.Models))
	for _, m := range upstream.Models {
		// Only text/chat models — those exposing generateContent.
		if !containsString(m.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		if id == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	sortOptions(options)
	return options, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// === Open WebUI (self-hosted, caller-supplied endpoint) ===

func (s *Service) getOpenWebUIModels(c *gin.Context) {
	endpoint := strings.TrimSpace(c.Query("endpoint"))
	if endpoint == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Server URL to load its model list"})
		return
	}
	upstreamURL, err := openWebUIModelsURL(endpoint)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Server URL must be a full http(s) URL"})
		return
	}
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchOpenWebUIModels(c, upstreamURL, apiKey)
	modelListOptionResponse(c, "Open WebUI", options, err)
}

// openWebUIModelsURL normalises a user-supplied base/endpoint into the
// OpenAI-compatible /models URL, built via url.URL (not string concat) so a
// crafted endpoint can't displace the forced path into the query string.
func openWebUIModelsURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("endpoint must be a full http(s) URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/v1/models", "/models", "/v1", "/api"} {
		path = strings.TrimSuffix(path, suffix)
	}
	parsed.Path = path + "/api/models"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

func fetchOpenWebUIModels(c *gin.Context, upstreamURL, apiKey string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// Reuse the SSRF-guarded client — the host is caller-supplied.
	resp, err := ollamaModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &aiModelsStatusError{provider: "Open WebUI", status: resp.StatusCode}
	}

	// Open WebUI's /api/models returns {data:[{id, name}]}.
	var upstream struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}
	options := make([]api.InputOption, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		options = append(options, api.InputOption{Name: name, Value: m.ID})
	}
	sortOptions(options)
	return options, nil
}
