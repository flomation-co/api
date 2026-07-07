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

// asanaAPIBase is the fixed Asana REST root the option proxies call. The Asana
// host is a constant (never caller-supplied), so — like the Trello proxies —
// there is NO SSRF surface here and the client needs no dial Control /
// metadata-IP guard. Kept in sync with the executor's asana_common.APIBase.
const asanaAPIBase = "https://app.asana.com/api/1.0"

// asanaOptionsHTTPClient is shared across the Asana dropdown proxies so
// connections to app.asana.com are pooled. Cross-host redirects are refused
// defensively.
var asanaOptionsHTTPClient = &gohttp.Client{
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

// getAsanaWorkspaces serves the token's workspaces as dropdown options. No
// dependency inputs.
func (s *Service) getAsanaWorkspaces(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("opt_fields", "name")
	q.Set("limit", "100")
	s.serveAsanaNamed(c, token, "/workspaces", q)
}

// getAsanaProjects serves the selected workspace's projects. Depends on
// "workspace".
func (s *Service) getAsanaProjects(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	workspace, ok := asanaRequireDependency(c, "workspace", "Select a Workspace to load its projects")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("workspace", workspace)
	q.Set("opt_fields", "name")
	q.Set("limit", "100")
	s.serveAsanaNamed(c, token, "/projects", q)
}

// getAsanaUsers serves the token's users as dropdown options. The workspace is
// optional — GET /users returns the users the token can access, narrowed to a
// workspace when one is supplied (so this works on actions without a workspace
// field, e.g. task update's assignee).
func (s *Service) getAsanaUsers(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("opt_fields", "name")
	// Asana rejects `limit` on GET /users unless a workspace is given
	// ("Need to specify a workspace to paginate!"), so only paginate when we can
	// scope it; unscoped, GET /users returns all accessible users unpaginated.
	if ws := strings.TrimSpace(c.Query("workspace")); ws != "" && !strings.HasPrefix(ws, "${") {
		q.Set("workspace", ws)
		q.Set("limit", "100")
	}
	s.serveAsanaNamed(c, token, "/users", q)
}

// getAsanaSections serves the selected project's sections. Depends on the chosen
// project, forwarded as "project_id" (or "project" for task get-many).
func (s *Service) getAsanaSections(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	project := strings.TrimSpace(c.Query("project_id"))
	if project == "" || strings.HasPrefix(project, "${") {
		project = strings.TrimSpace(c.Query("project"))
	}
	if project == "" || strings.HasPrefix(project, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load its sections"})
		return
	}
	q := url.Values{}
	q.Set("opt_fields", "name")
	q.Set("limit", "100")
	s.serveAsanaNamed(c, token, "/projects/"+url.PathEscape(project)+"/sections", q)
}

// getAsanaTags serves the selected workspace's tags. Depends on "workspace".
func (s *Service) getAsanaTags(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	workspace, ok := asanaRequireDependency(c, "workspace", "Select a Workspace to load its tags")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("workspace", workspace)
	q.Set("opt_fields", "name")
	q.Set("limit", "100")
	s.serveAsanaNamed(c, token, "/tags", q)
}

// getAsanaTeams serves the selected workspace's teams. Depends on "workspace".
// Teams live under the organization endpoint.
func (s *Service) getAsanaTeams(c *gin.Context) {
	token, ok := s.resolveAsanaToken(c)
	if !ok {
		return
	}
	workspace, ok := asanaRequireDependency(c, "workspace", "Select a Workspace to load its teams")
	if !ok {
		return
	}
	q := url.Values{}
	q.Set("opt_fields", "name")
	q.Set("limit", "100")
	s.serveAsanaNamed(c, token, "/organizations/"+url.PathEscape(workspace)+"/teams", q)
}

// asanaRequireDependency reads a dependency query param and writes the
// option-proxy "pick the parent first" message when it is blank or an unresolved
// ${...} reference.
func asanaRequireDependency(c *gin.Context, name, prompt string) (string, bool) {
	v := strings.TrimSpace(c.Query(name))
	if v == "" || strings.HasPrefix(v, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": prompt})
		return "", false
	}
	return v, true
}

// resolveAsanaToken pulls the node's Asana access token from the query params.
// The token is a secret that may arrive as a ${secrets.X} reference resolved
// server-side. On any problem it writes the option-proxy error response (HTTP
// 200 + {"error": …}) and returns ok=false so the editor shows the message
// inline and falls back to manual entry.
func (s *Service) resolveAsanaToken(c *gin.Context) (string, bool) {
	token := strings.TrimSpace(c.Query("access_token"))

	if strings.HasPrefix(token, "${credentials.") || strings.HasPrefix(token, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the Access Token (the flow itself still runs)"})
		return "", false
	}
	if strings.HasPrefix(token, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the Access Token secret"})
			return "", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, token)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", false
		}
		token = resolved
	}
	if token == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Access Token to load this list"})
		return "", false
	}
	return token, true
}

// serveAsanaNamed performs a Bearer-authenticated GET against the fixed Asana
// host and returns the {data:[{gid,name}]} rows as sorted dropdown options.
func (s *Service) serveAsanaNamed(c *gin.Context, token, path string, q url.Values) {
	reqURL := asanaAPIBase + path
	if enc := q.Encode(); enc != "" {
		reqURL += "?" + enc
	}
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Asana request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := asanaOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Asana for options")
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not reach Asana — check your connection and try again"})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Asana rejected the request as unauthorised — check the Access Token"})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Asana returned an unexpected response (HTTP " + strings.TrimSpace(gohttp.StatusText(resp.StatusCode)) + ")"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to read the Asana response"})
		return
	}
	var env struct {
		Data []struct {
			GID  string `json:"gid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Asana response"})
		return
	}
	options := make([]api.InputOption, 0, len(env.Data))
	for _, r := range env.Data {
		if r.GID == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = r.GID
		}
		options = append(options, api.InputOption{Name: name, Value: r.GID})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}
