package http

import (
	"encoding/json"
	"errors"
	"io"
	gohttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// intercomOptionHosts maps the node's Region input to the fixed Intercom REST
// host the option proxies call. The hosts are constants (never
// caller-supplied), so — like the Asana/Trello proxies — there is NO SSRF
// surface here and the client needs no dial Control / metadata-IP guard. Kept
// in sync with the executor's intercom common BaseURL map.
var intercomOptionHosts = map[string]string{
	"us": "https://api.intercom.io",
	"eu": "https://api.eu.intercom.io",
	"au": "https://api.au.intercom.io",
}

// intercomAPIVersion pins every proxy request to the same API version the
// executor actions send, so the dropdowns and the flow see identical shapes.
const intercomAPIVersion = "2.15"

// intercomOptionsHTTPClient is shared across the Intercom dropdown proxies so
// connections to the regional API hosts are pooled. Cross-host redirects are
// refused defensively.
var intercomOptionsHTTPClient = &gohttp.Client{
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

// getIntercomAdmins serves the workspace's teammates as "Name (email)" options
// for the admin / author / assignee pickers.
func (s *Service) getIntercomAdmins(c *gin.Context) {
	auth, ok := s.resolveIntercomAuth(c)
	if !ok {
		return
	}
	rows, errMsg := fetchIntercomRows(c, auth, gohttp.MethodGet, "/admins", nil, "admins")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := intercomStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(intercomStr(r["name"]))
		if name == "" {
			name = id
		}
		if email := strings.TrimSpace(intercomStr(r["email"])); email != "" {
			name += " (" + email + ")"
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeIntercomOptions(c, options)
}

// getIntercomTeams serves the workspace's teams for the team pickers.
func (s *Service) getIntercomTeams(c *gin.Context) {
	s.serveIntercomNamed(c, gohttp.MethodGet, "/teams", nil, "teams", "name")
}

// getIntercomTags serves the workspace's tags for the tag pickers.
func (s *Service) getIntercomTags(c *gin.Context) {
	s.serveIntercomNamed(c, gohttp.MethodGet, "/tags", nil, "data", "name")
}

// getIntercomTicketTypes serves the workspace's ticket types.
func (s *Service) getIntercomTicketTypes(c *gin.Context) {
	s.serveIntercomNamed(c, gohttp.MethodGet, "/ticket_types", nil, "data", "name")
}

// getIntercomTicketStates serves the workspace's ticket states. States carry an
// internal (teammate-facing) and external (customer-facing) label — the
// internal one is what operators see in the Intercom inbox, so prefer it.
func (s *Service) getIntercomTicketStates(c *gin.Context) {
	auth, ok := s.resolveIntercomAuth(c)
	if !ok {
		return
	}
	rows, errMsg := fetchIntercomRows(c, auth, gohttp.MethodGet, "/ticket_states", nil, "data")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := intercomStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(intercomStr(r["internal_label"]))
		if name == "" {
			name = strings.TrimSpace(intercomStr(r["external_label"]))
		}
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeIntercomOptions(c, options)
}

// getIntercomSegments serves the workspace's segments.
func (s *Service) getIntercomSegments(c *gin.Context) {
	s.serveIntercomNamed(c, gohttp.MethodGet, "/segments", nil, "segments", "name")
}

// getIntercomCompanies serves the workspace's companies as options whose value
// is the INTERCOM company id (what attach/detach/tag expect — not the caller's
// own company_id). The cursor list endpoint is a POST; one 150-row page covers
// realistic picker sizes and bounds the edit-time fetch (beyond it the picker
// still falls back to manual entry).
func (s *Service) getIntercomCompanies(c *gin.Context) {
	auth, ok := s.resolveIntercomAuth(c)
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("per_page", "150")
	rows, errMsg := fetchIntercomRows(c, auth, gohttp.MethodPost, "/companies/list", q, "data")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := intercomStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(intercomStr(r["name"]))
		if name == "" {
			name = strings.TrimSpace(intercomStr(r["company_id"]))
		}
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeIntercomOptions(c, options)
}

// getIntercomCollections serves the Help Center collections for the article
// parent pickers. Like the companies proxy, one 150-row page (Intercom's max
// per_page — the endpoint's un-tuned default is only 20) covers realistic
// picker sizes and bounds the edit-time fetch; beyond it the picker still
// falls back to manual entry.
func (s *Service) getIntercomCollections(c *gin.Context) {
	q := url.Values{}
	q.Set("per_page", "150")
	s.serveIntercomNamed(c, gohttp.MethodGet, "/help_center/collections", q, "data", "name")
}

// serveIntercomNamed is the common path for lists whose options are a plain
// {nameField, id} mapping: resolve auth, fetch the named array, map and write.
func (s *Service) serveIntercomNamed(c *gin.Context, method, path string, query url.Values, arrayKey, nameField string) {
	auth, ok := s.resolveIntercomAuth(c)
	if !ok {
		return
	}
	rows, errMsg := fetchIntercomRows(c, auth, method, path, query, arrayKey)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := intercomStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(intercomStr(r[nameField]))
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeIntercomOptions(c, options)
}

// intercomAuth is the resolved connection the proxies call Intercom with: the
// fixed regional host plus the plaintext access token.
type intercomAuth struct {
	host  string
	token string
}

// resolveIntercomAuth pulls the node's Region and Access Token from the query
// params. The region selects a fixed host (empty/unknown/unresolved values fall
// back to US, which auto-routes); the token is a secret that may arrive as a
// ${secrets.X} reference resolved server-side behind the EnvironmentView
// permission gate. On any problem it writes the option-proxy error response
// (HTTP 200 + {"error": …}) and returns ok=false so the editor shows the
// message inline and falls back to manual entry.
func (s *Service) resolveIntercomAuth(c *gin.Context) (intercomAuth, bool) {
	region := strings.ToLower(strings.TrimSpace(c.Query("region")))
	host, ok := intercomOptionHosts[region]
	if !ok {
		host = intercomOptionHosts["us"]
	}

	token := strings.TrimSpace(c.Query("api_token"))
	if strings.HasPrefix(token, "${credentials.") || strings.HasPrefix(token, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the Access Token (the flow itself still runs)"})
		return intercomAuth{}, false
	}
	if strings.HasPrefix(token, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the Access Token secret"})
			return intercomAuth{}, false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return intercomAuth{}, false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, token)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return intercomAuth{}, false
		}
		token = resolved
	}
	if token == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Access Token to load this list"})
		return intercomAuth{}, false
	}
	return intercomAuth{host: host, token: token}, true
}

// fetchIntercomRows performs a Bearer-authenticated request against the fixed
// regional host and returns the named top-level array as generic rows. When the
// named key is absent it falls back to "data", then to the response's only
// array-typed key — Intercom's list envelopes name their arrays per resource.
func fetchIntercomRows(c *gin.Context, auth intercomAuth, method, path string, query url.Values, arrayKey string) ([]map[string]interface{}, string) {
	reqURL := auth.host + path
	if enc := query.Encode(); enc != "" {
		reqURL += "?" + enc
	}
	var reqBody io.Reader
	if method == gohttp.MethodPost {
		reqBody = strings.NewReader("{}")
	}
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), method, reqURL, reqBody)
	if err != nil {
		return nil, "Could not build the Intercom request"
	}
	req.Header.Set("Authorization", "Bearer "+auth.token)
	req.Header.Set("Intercom-Version", intercomAPIVersion)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := intercomOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Intercom for options")
		return nil, "Could not reach Intercom — check your connection and try again"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, "Intercom rejected the request as unauthorised — check the Access Token and Region"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "Intercom returned an unexpected response (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Failed to read the Intercom response"
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "Failed to parse the Intercom response"
	}
	raw := env[arrayKey]
	if !intercomIsArray(raw) {
		raw = env["data"]
	}
	if !intercomIsArray(raw) {
		// Last-resort fallback for an unexpected envelope: take the first
		// non-pages array, scanning keys in sorted order so the pick is
		// deterministic even if a response ever carried two arrays. Every
		// proxy passes an explicit arrayKey, so this is defence, not routing.
		keys := make([]string, 0, len(env))
		for key := range env {
			if key != "pages" && intercomIsArray(env[key]) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			raw = env[keys[0]]
		}
	}
	if !intercomIsArray(raw) {
		return nil, "Failed to parse the Intercom response"
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, "Failed to parse the Intercom response"
	}
	return rows, ""
}

// intercomIsArray reports whether a raw JSON value is an array (Intercom
// envelopes mix arrays with scalar/object siblings like pages and total_count).
func intercomIsArray(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "[")
}

// writeIntercomOptions sorts the options by name and writes the option-proxy
// success envelope.
func writeIntercomOptions(c *gin.Context, options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// intercomStr renders an id/label field that may arrive as a JSON string or
// number (Intercom mixes both across resources) as a plain string.
func intercomStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
