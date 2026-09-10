package http

import (
	"encoding/json"
	"io"
	gohttp "net/http"
	"strings"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
)

// This file backs the live "Avatar" and "Voice" dropdowns on the HeyGen video
// actions. Each is a dynamic-options proxy: the editor forwards the node's
// api_key and the api fetches HeyGen's avatar/voice list server-side, so a
// paste-a-key action offers a live dropdown without the plaintext key ever
// transiting the browser. Same convention as ai_models.go: HTTP 200 with either
// {"options": [{name, value}]} or {"error": "..."}.

// Upstream list URLs are vars so tests can point them at an httptest server.
var (
	heygenAvatarsURL = "https://api.heygen.com/v3/avatars"
	heygenVoicesURL  = "https://api.heygen.com/v3/voices"
)

func (s *Service) getHeyGenAvatars(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchHeyGenList(c, heygenAvatarsURL, apiKey, "avatars",
		[]string{"avatar_id", "id"}, []string{"avatar_name", "name"})
	modelListOptionResponse(c, "HeyGen", options, err)
}

func (s *Service) getHeyGenVoices(c *gin.Context) {
	apiKey, ok := s.resolveOptionAPIKey(c, c.Query("api_key"))
	if !ok {
		return
	}
	options, err := fetchHeyGenList(c, heygenVoicesURL, apiKey, "voices",
		[]string{"voice_id", "id"}, []string{"name", "display_name"})
	modelListOptionResponse(c, "HeyGen", options, err)
}

// fetchHeyGenList calls a HeyGen v3 list endpoint and maps the records under
// data.<listKey> into options, reading the id and label from the first present
// of idKeys / nameKeys respectively. A voice record additionally annotates the
// label with its language when available.
func fetchHeyGenList(c *gin.Context, url, apiKey, listKey string, idKeys, nameKeys []string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")

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
		return nil, &aiModelsStatusError{provider: "HeyGen", status: resp.StatusCode}
	}

	var upstream struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}
	var records []map[string]interface{}
	if raw, ok := upstream.Data[listKey]; ok {
		_ = json.Unmarshal(raw, &records)
	}

	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		id := firstString(r, idKeys...)
		if id == "" {
			continue
		}
		name := firstString(r, nameKeys...)
		if name == "" {
			name = id
		}
		if lang := firstString(r, "language", "locale"); lang != "" {
			name = name + " (" + lang + ")"
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	sortOptions(options)
	return options, nil
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
