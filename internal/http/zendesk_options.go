package http

import (
	"encoding/base64"
	"encoding/json"
	gohttp "net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// zendeskOptionsHTTPClient is shared across the Zendesk dropdown proxies so
// connections to accounts' API servers are pooled. The timeout is short: the
// editor is waiting on this to render a dropdown.
var zendeskOptionsHTTPClient = &gohttp.Client{Timeout: 10 * time.Second}

// validZendeskSubdomain matches a bare account handle. The subdomain forms the
// host of a server-side request, so it is validated to letters/numbers/hyphens
// before use — a crafted value can never point the proxy off zendesk.com.
var validZendeskSubdomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// zendeskOptionsHostOverride, when non-empty, replaces the per-subdomain
// https://{sub}.zendesk.com base so tests can point the fetch at an httptest
// server.
var zendeskOptionsHostOverride = ""

// getZendeskGroups serves the account's agent groups as dropdown options for
// the ticket group_id inputs.
func (s *Service) getZendeskGroups(c *gin.Context) {
	s.serveZendeskOptions(c, "/groups.json", "groups")
}

// getZendeskOrganizations serves the account's organizations as dropdown
// options for the user organization_id inputs.
func (s *Service) getZendeskOrganizations(c *gin.Context) {
	s.serveZendeskOptions(c, "/organizations.json", "organizations")
}

// serveZendeskOptions resolves the node's Zendesk credentials from the query
// params (subdomain + email plain, api_token a secret reference resolved
// server-side), calls the account's API with Basic auth, and returns the named
// array as {name, value} options. Errors follow the option-proxy convention of
// HTTP 200 + {"error": ...} so the editor shows the message inline and falls
// back to manual entry.
func (s *Service) serveZendeskOptions(c *gin.Context, path, arrayKey string) {
	subdomain := normaliseZendeskSubdomainAPI(c.Query("subdomain"))
	if subdomain == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Subdomain to load this list"})
		return
	}
	if !validZendeskSubdomain.MatchString(subdomain) {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Subdomain must be your Zendesk account handle (letters, numbers, hyphens)"})
		return
	}

	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Agent Email to load this list"})
		return
	}

	apiToken := strings.TrimSpace(c.Query("api_token"))
	if strings.HasPrefix(apiToken, "${credentials.") || strings.HasPrefix(apiToken, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the API token (the flow itself still runs)"})
		return
	}
	if strings.HasPrefix(apiToken, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API token secret"})
			return
		}
		// Resolving a secret to plaintext here must be gated by the same
		// permission as reading it through the environment endpoints, since the
		// resolved value is used to authenticate a request on the user's behalf.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, apiToken)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		apiToken = resolved
	}
	if apiToken == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Token to load this list"})
		return
	}

	options, errMsg := fetchZendeskOptions(c, subdomain, email, apiToken, path, arrayKey)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// fetchZendeskOptions calls the account's API and maps the named array of
// {id, name} objects into {name, value} options sorted by name.
func fetchZendeskOptions(c *gin.Context, subdomain, email, apiToken, path, arrayKey string) ([]api.InputOption, string) {
	base := "https://" + subdomain + ".zendesk.com"
	if zendeskOptionsHostOverride != "" {
		base = zendeskOptionsHostOverride
	}
	url := base + "/api/v2" + path + "?per_page=100"
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, url, nil)
	if err != nil {
		return nil, "Could not build the Zendesk request"
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(email+"/token:"+apiToken)))
	req.Header.Set("Accept", "application/json")

	resp, err := zendeskOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Zendesk for options")
		return nil, "Could not reach Zendesk — check the Subdomain"
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, "Zendesk rejected the request as unauthorised — check the Agent Email and API Token"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "Zendesk returned an unexpected response (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
	}

	var body struct {
		Groups        []zendeskNamed `json:"groups"`
		Organizations []zendeskNamed `json:"organizations"`
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&body); err != nil {
		return nil, "Failed to parse the Zendesk response"
	}

	var rows []zendeskNamed
	switch arrayKey {
	case "groups":
		rows = body.Groups
	case "organizations":
		rows = body.Organizations
	}

	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: r.Name, Value: strconv.FormatInt(r.ID, 10)})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options, ""
}

type zendeskNamed struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// normaliseZendeskSubdomainAPI reduces a pasted subdomain/URL to the bare
// handle, mirroring the executor's NormaliseSubdomain.
func normaliseZendeskSubdomainAPI(sub string) string {
	sub = strings.TrimSpace(sub)
	sub = strings.TrimPrefix(sub, "https://")
	sub = strings.TrimPrefix(sub, "http://")
	sub = strings.TrimRight(sub, "/")
	sub = strings.TrimSuffix(sub, ".zendesk.com")
	if i := strings.IndexAny(sub, "/.?#:@"); i >= 0 {
		sub = sub[:i]
	}
	return sub
}
