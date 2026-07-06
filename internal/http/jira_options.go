package http

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// jiraOptionsDialControl blocks link-local + cloud-metadata destinations on the
// address actually dialed (the Jira site URL is caller-supplied). Same SSRF
// hardening as the Jenkins/WooCommerce/WordPress proxies; loopback and private
// LAN stay allowed for self-hosted (Data Center) instances.
func jiraOptionsDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("link-local addresses are not allowed")
	}
	if isCloudMetadataIP(ip) {
		return errors.New("cloud metadata addresses are not allowed")
	}
	return nil
}

// jiraOptionsHTTPClient is shared across the Jira dropdown proxies so
// connections to sites are pooled. The timeout is short: the editor waits on
// this to render a dropdown.
var jiraOptionsHTTPClient = &gohttp.Client{
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
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: jiraOptionsDialControl}).DialContext,
	},
}

// getJiraProjects serves the site's projects as dropdown options for the
// issue "project" pickers.
func (s *Service) getJiraProjects(c *gin.Context) {
	base, email, apiToken, ok := s.resolveJiraCredentials(c)
	if !ok {
		return
	}
	options, errMsg := fetchJiraProjects(c, base, email, apiToken)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getJiraIssueTypes serves the selected project's issue types as dropdown
// options for the issue "issue_type" picker. Depends on the chosen project,
// forwarded as the "project" query param.
func (s *Service) getJiraIssueTypes(c *gin.Context) {
	base, email, apiToken, ok := s.resolveJiraCredentials(c)
	if !ok {
		return
	}
	project := strings.TrimSpace(c.Query("project"))
	if project == "" || strings.HasPrefix(project, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load its issue types"})
		return
	}
	options, errMsg := fetchJiraIssueTypes(c, base, email, apiToken, project)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getJiraPriorities serves the site's priorities as dropdown options for the
// issue "priority" pickers.
func (s *Service) getJiraPriorities(c *gin.Context) {
	base, email, apiToken, ok := s.resolveJiraCredentials(c)
	if !ok {
		return
	}
	options, errMsg := fetchJiraPriorities(c, base, email, apiToken)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getJiraUsers serves the site's active users as dropdown options for the
// assignee/reporter/account_id pickers.
func (s *Service) getJiraUsers(c *gin.Context) {
	base, email, apiToken, ok := s.resolveJiraCredentials(c)
	if !ok {
		return
	}
	options, errMsg := fetchJiraUsers(c, base, email, apiToken)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getJiraStatuses serves the selected issue's available transitions as dropdown
// options for the issue "status" picker. Depends on the chosen issue, forwarded
// as the "issue_key" query param.
func (s *Service) getJiraStatuses(c *gin.Context) {
	base, email, apiToken, ok := s.resolveJiraCredentials(c)
	if !ok {
		return
	}
	issueKey := strings.TrimSpace(c.Query("issue_key"))
	if issueKey == "" || strings.HasPrefix(issueKey, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select an Issue to load its statuses"})
		return
	}
	options, errMsg := fetchJiraStatuses(c, base, email, apiToken, issueKey)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// resolveJiraCredentials pulls the node's Jira connection from the query params
// (site url + email plain; api_token as a secret reference resolved
// server-side) and returns the normalised base URL, email and plaintext token.
// On any problem it writes the option-proxy error response (HTTP 200 +
// {"error": …}) and returns ok=false so the editor shows the message inline and
// falls back to manual entry.
func (s *Service) resolveJiraCredentials(c *gin.Context) (base, email, apiToken string, ok bool) {
	base, err := jiraOptionsBaseURL(strings.TrimSpace(c.Query("url")))
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Jira URL (a full http(s) URL) to load this list"})
		return "", "", "", false
	}

	email = strings.TrimSpace(c.Query("email"))
	if email == "" || strings.HasPrefix(email, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Email to load this list"})
		return "", "", "", false
	}

	apiToken = strings.TrimSpace(c.Query("api_token"))
	// The canonical managed-credential reference is plural (${credentials.X});
	// the singular ${credential.X} is not an emitted format but is guarded
	// leniently so a mistyped/legacy reference still fails closed with a clear
	// message rather than being treated as a literal token. Mirrors the other
	// option proxies.
	if strings.HasPrefix(apiToken, "${credentials.") || strings.HasPrefix(apiToken, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the API Token (the flow itself still runs)"})
		return "", "", "", false
	}
	if strings.HasPrefix(apiToken, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API Token secret"})
			return "", "", "", false
		}
		// Resolving a secret to plaintext here must be gated by the same
		// permission as reading it through the environment endpoints, since the
		// resolved value is used to authenticate a request on the user's behalf.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", "", "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, apiToken)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", "", "", false
		}
		apiToken = resolved
	}
	if apiToken == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Token to load this list"})
		return "", "", "", false
	}
	return base, email, apiToken, true
}

// jiraOptionsBaseURL turns a user-supplied site URL into a clean
// scheme+host[+path] base (no trailing slash, no REST-API suffix), defaulting to
// https. Built via url.URL so a crafted base can't smuggle userinfo or a query
// into the server-side request.
func jiraOptionsBaseURL(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "${") {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("url must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be http or https")
	}
	u.User = nil
	path := strings.TrimRight(u.Path, "/")
	// Strip a pasted REST-API suffix so both a bare root and a full endpoint URL
	// normalise to the same base.
	for _, suffix := range []string{"/rest/api/2", "/rest/api/3", "/rest/api/latest"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// maxJiraOptionPages caps how many pages the paginated Jira dropdown proxies
// follow (project search, 50/page). A handful of pages covers realistic project
// counts while bounding an edit-time fetch; beyond it the list is truncated (the
// picker still falls back to manual entry).
const maxJiraOptionPages = 20

// doJiraGet performs a Basic-authenticated GET against the site's REST API and
// returns the raw body, translating transport/HTTP errors into the friendly
// option-proxy message. base already excludes the /rest/api/2 suffix; path is
// the REST path beginning with "/".
func doJiraGet(c *gin.Context, base, email, apiToken, path string) ([]byte, string) {
	reqURL := base + "/rest/api/2" + path

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "Could not build the Jira request"
	}
	req.SetBasicAuth(email, apiToken)
	req.Header.Set("Accept", "application/json")

	resp, err := jiraOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Jira for options")
		return nil, "Could not reach Jira — check the Jira URL and that the site is reachable"
	}
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		_ = resp.Body.Close()
		return nil, "Jira rejected the request as unauthorised — check the Email and API Token"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, "Jira returned an unexpected response (HTTP " + strconv.Itoa(code) + ")"
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	if err != nil {
		return nil, "Failed to read the Jira response"
	}
	return body, ""
}

type jiraNamed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func fetchJiraProjects(c *gin.Context, base, email, apiToken string) ([]api.InputOption, string) {
	var options []api.InputOption
	startAt := 0
	for page := 0; page < maxJiraOptionPages; page++ {
		q := url.Values{}
		q.Set("maxResults", "50")
		q.Set("startAt", strconv.Itoa(startAt))
		body, errMsg := doJiraGet(c, base, email, apiToken, "/project/search?"+q.Encode())
		if errMsg != "" {
			return nil, errMsg
		}
		var parsed struct {
			Values []struct {
				ID   string `json:"id"`
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"values"`
			IsLast     bool `json:"isLast"`
			MaxResults int  `json:"maxResults"`
			Total      int  `json:"total"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, "Failed to parse the Jira response"
		}
		for _, p := range parsed.Values {
			if p.ID == "" {
				continue
			}
			name := p.Name
			if p.Key != "" {
				name = p.Name + " (" + p.Key + ")"
			}
			options = append(options, api.InputOption{Name: name, Value: p.ID})
		}
		if parsed.IsLast || len(parsed.Values) == 0 {
			break
		}
		startAt += len(parsed.Values)
	}
	sortJiraOptions(options)
	return options, ""
}

func fetchJiraIssueTypes(c *gin.Context, base, email, apiToken, project string) ([]api.InputOption, string) {
	body, errMsg := doJiraGet(c, base, email, apiToken, "/project/"+url.PathEscape(project))
	if errMsg != "" {
		return nil, errMsg
	}
	var parsed struct {
		IssueTypes []jiraNamed `json:"issueTypes"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "Failed to parse the Jira response"
	}
	options := make([]api.InputOption, 0, len(parsed.IssueTypes))
	for _, it := range parsed.IssueTypes {
		if it.ID == "" || it.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: it.Name, Value: it.ID})
	}
	sortJiraOptions(options)
	return options, ""
}

func fetchJiraPriorities(c *gin.Context, base, email, apiToken string) ([]api.InputOption, string) {
	body, errMsg := doJiraGet(c, base, email, apiToken, "/priority")
	if errMsg != "" {
		return nil, errMsg
	}
	var rows []jiraNamed
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, "Failed to parse the Jira response"
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" || r.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: r.Name, Value: r.ID})
	}
	// Priorities have a meaningful order (Highest→Lowest); preserve it rather
	// than sorting alphabetically.
	return options, ""
}

func fetchJiraUsers(c *gin.Context, base, email, apiToken string) ([]api.InputOption, string) {
	body, errMsg := doJiraGet(c, base, email, apiToken, "/users/search?maxResults=100")
	if errMsg != "" {
		return nil, errMsg
	}
	var rows []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Active      bool   `json:"active"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, "Failed to parse the Jira response"
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if !r.Active || r.AccountID == "" || r.DisplayName == "" {
			continue
		}
		options = append(options, api.InputOption{Name: r.DisplayName, Value: r.AccountID})
	}
	sortJiraOptions(options)
	return options, ""
}

func fetchJiraStatuses(c *gin.Context, base, email, apiToken, issueKey string) ([]api.InputOption, string) {
	body, errMsg := doJiraGet(c, base, email, apiToken, "/issue/"+url.PathEscape(issueKey)+"/transitions")
	if errMsg != "" {
		return nil, errMsg
	}
	var parsed struct {
		Transitions []jiraNamed `json:"transitions"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "Failed to parse the Jira response"
	}
	options := make([]api.InputOption, 0, len(parsed.Transitions))
	for _, t := range parsed.Transitions {
		if t.ID == "" || t.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: t.Name, Value: t.ID})
	}
	return options, ""
}

func sortJiraOptions(options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
}
