package http

import (
	"encoding/json"
	"errors"
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

// trelloAPIBase is the fixed Trello REST root the option proxies call. Unlike
// the self-hosted WordPress/Jira/WooCommerce proxies, the Trello host is a
// constant (never caller-supplied), so there is NO SSRF surface here — every
// request targets api.trello.com and a crafted input cannot redirect it. That is
// why this file has no dial Control / metadata-IP guard: there is no
// caller-controlled host to guard against. Kept in sync with the executor's
// trello_common.APIBase and launch's trelloAPIBase.
const trelloAPIBase = "https://api.trello.com/1"

// trelloOptionsHTTPClient is shared across the Trello dropdown proxies so
// connections to api.trello.com are pooled. The timeout is short: the editor
// waits on this to render a dropdown. Cross-host redirects are refused defensively
// (Trello should never redirect an API call off-host).
var trelloOptionsHTTPClient = &gohttp.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *gohttp.Request, via []*gohttp.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after too many redirects")
		}
		if req.URL.Host != via[0].URL.Host {
			return errors.New("cross-host redirect not allowed")
		}
		return nil
	},
}

// getTrelloBoards serves the caller's boards as dropdown options for every
// board picker (board id / board_id inputs). No dependency inputs.
func (s *Service) getTrelloBoards(c *gin.Context) {
	key, token, ok := s.resolveTrelloCredentials(c)
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("fields", "name")
	q.Set("filter", "open")
	body, errMsg := s.doTrelloGet(c, key, token, "/members/me/boards", q)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := trelloNamedOptions(body)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getTrelloLists serves the selected board's lists as dropdown options for the
// list pickers. Depends on the chosen board (the "board_id" query param).
func (s *Service) getTrelloLists(c *gin.Context) {
	key, token, ok := s.resolveTrelloCredentials(c)
	if !ok {
		return
	}
	boardID, ok := trelloRequireDependency(c, "board_id", "Select a Board to load its lists")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("fields", "name")
	q.Set("filter", "open")
	body, errMsg := s.doTrelloGet(c, key, token, "/boards/"+url.PathEscape(boardID)+"/lists", q)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := trelloNamedOptions(body)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getTrelloLabels serves the selected board's labels as dropdown options for the
// label pickers. Depends on the chosen board (the "board_id" query param). A
// label may have no name (colour-only), so the colour is used as a fallback
// display so the option is never blank.
func (s *Service) getTrelloLabels(c *gin.Context) {
	key, token, ok := s.resolveTrelloCredentials(c)
	if !ok {
		return
	}
	boardID, ok := trelloRequireDependency(c, "board_id", "Select a Board to load its labels")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("fields", "name,color")
	q.Set("limit", "1000")
	body, errMsg := s.doTrelloGet(c, key, token, "/boards/"+url.PathEscape(boardID)+"/labels", q)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	var rows []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Trello response"})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		switch {
		case name == "" && r.Color != "":
			name = "(" + r.Color + ")"
		case name != "" && r.Color != "":
			name = r.Name + " (" + r.Color + ")"
		case name == "":
			name = r.ID
		}
		options = append(options, api.InputOption{Name: name, Value: r.ID})
	}
	sortTrelloOptions(options)
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getTrelloMembers serves the selected board's members as dropdown options for
// the board-member pickers. Depends on the chosen board (the "board_id" query
// param).
func (s *Service) getTrelloMembers(c *gin.Context) {
	key, token, ok := s.resolveTrelloCredentials(c)
	if !ok {
		return
	}
	boardID, ok := trelloRequireDependency(c, "board_id", "Select a Board to load its members")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("fields", "fullName,username")
	body, errMsg := s.doTrelloGet(c, key, token, "/boards/"+url.PathEscape(boardID)+"/members", q)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	var rows []struct {
		ID       string `json:"id"`
		FullName string `json:"fullName"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Trello response"})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		name := strings.TrimSpace(r.FullName)
		switch {
		case name == "" && r.Username != "":
			name = "@" + r.Username
		case name != "" && r.Username != "":
			name = r.FullName + " (@" + r.Username + ")"
		case name == "":
			name = r.ID
		}
		options = append(options, api.InputOption{Name: name, Value: r.ID})
	}
	sortTrelloOptions(options)
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// trelloRequireDependency reads a dependency query param (e.g. board_id) and
// writes the option-proxy "pick the parent first" message when it is blank or an
// unresolved ${...} reference. Returns ok=false in that case.
func trelloRequireDependency(c *gin.Context, name, prompt string) (string, bool) {
	v := strings.TrimSpace(c.Query(name))
	if v == "" || strings.HasPrefix(v, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": prompt})
		return "", false
	}
	return v, true
}

// resolveTrelloCredentials pulls the node's Trello connection from the query
// params. BOTH the API key and token are secrets (unlike Jira, where only the
// token is), so each may arrive as a ${secrets.X} reference resolved server-side.
// On any problem it writes the option-proxy error response (HTTP 200 + {"error":
// …}) and returns ok=false so the editor shows the message inline and falls back
// to manual entry.
func (s *Service) resolveTrelloCredentials(c *gin.Context) (key, token string, ok bool) {
	key = strings.TrimSpace(c.Query("api_key"))
	token = strings.TrimSpace(c.Query("api_token"))

	// Managed credentials can't be resolved to plaintext here.
	for _, v := range []string{key, token} {
		if strings.HasPrefix(v, "${credentials.") || strings.HasPrefix(v, "${credential.") {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use environment secrets for the API Key/Token (the flow itself still runs)"})
			return "", "", false
		}
	}

	if strings.HasPrefix(key, "${") || strings.HasPrefix(token, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API Key/Token secrets"})
			return "", "", false
		}
		// Resolving a secret to plaintext must be gated by the same permission as
		// reading it through the environment endpoints, since the resolved value
		// authenticates a request on the user's behalf.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", "", false // checkPermission has written the response
		}
		if strings.HasPrefix(key, "${") {
			resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, key)
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
				return "", "", false
			}
			key = resolved
		}
		if strings.HasPrefix(token, "${") {
			resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, token)
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
				return "", "", false
			}
			token = resolved
		}
	}
	if key == "" || token == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Key and API Token to load this list"})
		return "", "", false
	}
	return key, token, true
}

// doTrelloGet performs a key+token-authenticated GET against the fixed Trello
// API host and returns the raw body, translating transport/HTTP errors into the
// friendly option-proxy message. The key and token are appended as query params
// (Trello's auth scheme); callers pass only the operation's own params.
func (s *Service) doTrelloGet(c *gin.Context, key, token, path string, q url.Values) ([]byte, string) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("key", key)
	q.Set("token", token)
	reqURL := trelloAPIBase + path + "?" + q.Encode()

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "Could not build the Trello request"
	}
	req.Header.Set("Accept", "application/json")

	resp, err := trelloOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Trello for options")
		return nil, "Could not reach Trello — check your connection and try again"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, "Trello rejected the request as unauthorised — check the API Key and API Token"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "Trello returned an unexpected response (HTTP " + strings.TrimSpace(gohttp.StatusText(resp.StatusCode)) + ")"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Failed to read the Trello response"
	}
	return body, ""
}

// trelloNamedOptions decodes a Trello array of {id,name} into sorted dropdown
// options (used for boards and lists, which always carry a name).
func trelloNamedOptions(body []byte) ([]api.InputOption, string) {
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, "Failed to parse the Trello response"
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = r.ID
		}
		options = append(options, api.InputOption{Name: name, Value: r.ID})
	}
	sortTrelloOptions(options)
	return options, ""
}

func sortTrelloOptions(options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
}
