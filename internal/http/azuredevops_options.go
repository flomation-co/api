package http

import (
	"encoding/base64"
	"encoding/json"
	"io"
	gohttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Azure DevOps — live dropdowns
// ---------------------------------------------------------------------------
//
// Azure DevOps identifies almost everything by GUID, and its UI shows names.
// Without these proxies an operator would have to open the portal, find a
// repository, and copy a GUID out of the URL — for every field, on every node.
//
// Auth is the whole reason the executor node hand-rolls REST rather than
// taking the SDK: it is Basic with an EMPTY username and the PAT as the
// password. The empty username is not decorative — ":"+PAT is what the service
// expects, and a PAT placed in the username half silently 203s (see below).

// azureDevOpsConnParams are the connection inputs the editor forwards for the
// project list.
//
// `environment` is deliberately absent, matching the wave-1 Azure lists: the
// editor injects it itself after walking Params (propertyMenu/index.tsx),
// because it is not an input on the node. The codebase is genuinely split on
// this — asana, intercom, awx, trello and sendgrid all declare it — and it is
// harmless either way, because the editor's loop sets a declared `environment`
// to "" (no sibling input matches) and the injection immediately overwrites
// it. Not worth churning those nodes; just don't read their lists as a
// convention.
var azureDevOpsConnParams = []string{"organisation_url", "personal_access_token", "api_version"}

// azureDevOpsProjectParams additionally carry the project, for the lists that
// are scoped to one (repositories, pipelines, release definitions, teams).
var azureDevOpsProjectParams = []string{"organisation_url", "personal_access_token", "api_version", "project"}

// azureDevOpsProjectActions are the azuredevops actions with a `project` input
// naming an existing project — every action carrying one except project_get_all
// (which lists them) and project_create (which names a new one).
var azureDevOpsProjectActions = []string{
	"branch_get_all", "build_cancel", "build_get", "build_get_all", "build_log_get",
	"commit_get_all", "pipeline_artifact_get", "pipeline_get_all", "pipeline_run",
	"pipeline_run_get", "pipeline_run_get_all", "pr_comment_add", "pr_complete",
	"pr_create", "pr_get", "pr_get_all", "pr_update", "project_get",
	"release_create", "release_get_all", "repo_get", "repo_get_all", "team_get_all",
	"workitem_comment_add", "workitem_comment_get_all", "workitem_create",
	"workitem_delete", "workitem_get", "workitem_get_batch", "workitem_query_wiql",
	"workitem_type_get_all", "workitem_update",
}

// azureDevOpsRepositoryActions are the actions with a `repository` input naming
// an existing repository.
var azureDevOpsRepositoryActions = []string{
	"branch_get_all", "commit_get_all", "pr_comment_add", "pr_complete",
	"pr_create", "pr_get", "pr_get_all", "pr_update", "repo_get",
}

// azureDevOpsPipelineActions are the actions with a `pipeline_id` input.
var azureDevOpsPipelineActions = []string{
	"pipeline_artifact_get", "pipeline_run", "pipeline_run_get", "pipeline_run_get_all",
}

// azureDevOpsDefinitionActions are the classic-Release actions with a
// `definition_id` input. These resolve from the vsrm host, not dev.azure.com.
var azureDevOpsDefinitionActions = []string{"release_create", "release_get_all"}

func init() {
	register := func(actionID, input, endpoint string, params []string) {
		dynamicOptionsMetadata[actionID+"#"+input] = api.InputDynamicOptions{
			Endpoint: "/api/v1/action/options/" + endpoint,
			Params:   params,
		}
	}

	for _, a := range azureDevOpsProjectActions {
		register("devops/azuredevops/"+a, "project", "azuredevops-projects", azureDevOpsConnParams)
	}
	for _, a := range azureDevOpsRepositoryActions {
		register("devops/azuredevops/"+a, "repository", "azuredevops-repositories", azureDevOpsProjectParams)
	}
	for _, a := range azureDevOpsPipelineActions {
		register("devops/azuredevops/"+a, "pipeline_id", "azuredevops-pipelines", azureDevOpsProjectParams)
	}
	for _, a := range azureDevOpsDefinitionActions {
		register("devops/azuredevops/"+a, "definition_id", "azuredevops-release-definitions", azureDevOpsProjectParams)
	}
	register("devops/azuredevops/workitem_query_wiql", "team", "azuredevops-teams", azureDevOpsProjectParams)
}

// azureDevOpsBases mirrors the executor's normaliseOrgURL: it reduces a pasted
// organisation URL to the core base and its vsrm twin, accepting both live
// shapes (https://dev.azure.com/{org} and the legacy
// https://{org}.visualstudio.com, whose Release hosts differ in shape as well
// as name). Host is lower-cased (DNS is case-insensitive); the path is NOT,
// because project names in paths are case-sensitive. Query, fragment and any
// smuggled userinfo are dropped so a crafted value cannot append itself to
// every request.
// The final return is an OPERATOR-FACING message, not a Go error — see the note
// on parseAzureServiceBusConnString. Empty means success.
func azureDevOpsBases(raw string) (coreBase, releaseBase, errMsg string) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "${") {
		return "", "", "Set the Organisation URL to load this list"
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, parseErr := url.Parse(s)
	if parseErr != nil || u.Host == "" {
		return "", "", azureDevOpsOrgInvalidMsg
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", azureDevOpsOrgInvalidMsg
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" {
		host += ":" + u.Port()
	}
	path := strings.Trim(u.EscapedPath(), "/")

	switch {
	case strings.HasSuffix(host, ".visualstudio.com"):
		org := strings.TrimSuffix(host, ".visualstudio.com")
		if org == "" {
			return "", "", azureDevOpsOrgInvalidMsg
		}
		return u.Scheme + "://" + host, u.Scheme + "://" + org + ".vsrm.visualstudio.com", ""
	default:
		// dev.azure.com/{org} — the org is the first path segment.
		seg := strings.SplitN(path, "/", 2)
		if len(seg) == 0 || seg[0] == "" {
			return "", "", azureDevOpsOrgInvalidMsg
		}
		core := u.Scheme + "://" + host + "/" + seg[0]
		release := u.Scheme + "://vsrm." + host + "/" + seg[0]
		return core, release, ""
	}
}

const azureDevOpsOrgInvalidMsg = "The Organisation URL must look like https://dev.azure.com/your-org"

// azureDevOpsAuthHeader builds the Basic header for a PAT: an empty username
// with the PAT as the password. This is the form Microsoft documents.
//
// (Measured against dev.azure.com on 17/07/2026: the service also accepts the
// PAT in the USERNAME half — "PAT:" authenticates just as well as ":PAT". So
// this is the documented shape rather than the only working one; don't expect a
// mis-built header to be the cause of an auth failure.)
func azureDevOpsAuthHeader(pat string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat))
}

// azureDevOpsAPIVersion resolves the caller's optional pin, defaulting to the
// same version the executor sends. Pinned to a conservative pattern because it
// is interpolated into the query string.
func azureDevOpsAPIVersion(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || strings.HasPrefix(v, "${") {
		return "7.1"
	}
	for _, r := range v {
		isDigit := r >= '0' && r <= '9'
		isLower := r >= 'a' && r <= 'z'
		if !isDigit && !isLower && r != '.' && r != '-' {
			return "7.1"
		}
	}
	return v
}

// azureDevOpsProject validates the project name/GUID before it is placed in a
// URL path. Azure DevOps project names allow spaces and unicode, so this is
// deliberately permissive about content and strict only about the characters
// that would let a value escape its path segment.
func azureDevOpsProject(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	if p == "" || strings.HasPrefix(p, "${") {
		return "", false
	}
	if strings.ContainsAny(p, "/?#\\") {
		return "", false
	}
	return p, true
}

// doAzureDevOpsGet issues one authenticated GET and decodes the JSON envelope.
// Every list endpoint here returns {count, value:[...]}.
func doAzureDevOpsGet(c *gin.Context, endpoint, pat string, out interface{}) bool {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, endpoint, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Azure DevOps request"})
		return false
	}
	req.Header.Set("Authorization", azureDevOpsAuthHeader(pat))
	req.Header.Set("Accept", "application/json")

	resp, err := azureDevOpsDo(req)
	if err != nil {
		// A bad or expired PAT does NOT come back as 401. Azure DevOps answers
		// with 302 to a sign-in page on ANOTHER host
		// (spsproduks1.vssps.visualstudio.com/_signin), and the shared redirect
		// guard correctly refuses to follow a cross-host redirect — so the
		// transport fails and, without this, an expired token surfaced as
		// "Could not reach Azure DevOps", sending the operator to debug their
		// network. Measured against dev.azure.com 17/07/2026.
		if isAzureDevOpsSignInRedirect(err) {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Azure DevOps rejected the credentials — check the Personal Access Token and that it has not expired"})
			return false
		}
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not reach Azure DevOps — check the Organisation URL"})
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == gohttp.StatusUnauthorized ||
		resp.StatusCode == gohttp.StatusForbidden ||
		// 203 + a sign-in page is the other unauthenticated shape this API is
		// documented to return; keep it mapped even though 302 is what the
		// current service actually sends.
		resp.StatusCode == gohttp.StatusNonAuthoritativeInfo {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Azure DevOps rejected the credentials — check the Personal Access Token and that it has not expired"})
		return false
	}
	// If the redirect guard is ever relaxed, a sign-in 302 would arrive here
	// rather than as a transport error.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Azure DevOps rejected the credentials — check the Personal Access Token and that it has not expired"})
		return false
	}
	if resp.StatusCode == gohttp.StatusNotFound {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Azure DevOps could not find that organisation or project — check the Organisation URL and Project"})
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Azure DevOps returned an unexpected response (HTTP " + strconv.Itoa(resp.StatusCode) + ")"})
		return false
	}
	if err := json.NewDecoder(limitBody(resp)).Decode(out); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Azure DevOps response"})
		return false
	}
	return true
}

// azureDevOpsDo is a package-level seam so tests can drive doAzureDevOpsGet's
// error handling without reaching the network.
var azureDevOpsDo = func(req *gohttp.Request) (*gohttp.Response, error) {
	return azureOptionsHTTPClient.Do(req)
}

// isAzureDevOpsSignInRedirect recognises the transport error the redirect guard
// raises when Azure DevOps bounces an unauthenticated call to its sign-in host.
// Matching on the guard's own message keeps the two coupled deliberately: if
// azureOptionsRedirect's wording changes, the test below fails rather than this
// silently degrading back to "Could not reach Azure DevOps".
func isAzureDevOpsSignInRedirect(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cross-host redirect not allowed")
}

// limitBody caps how much of a provider response is read, matching the ceiling
// doAzureOptionsGet applies. A dropdown list is kilobytes; anything approaching
// this is a misconfigured endpoint, not a big organisation.
func limitBody(resp *gohttp.Response) io.Reader {
	return io.LimitReader(resp.Body, 8<<20)
}

// azureDevOpsResolve pulls the org bases and PAT that every proxy here needs.
func (s *Service) azureDevOpsResolve(c *gin.Context) (coreBase, releaseBase, pat, version string, ok bool) {
	coreBase, releaseBase, errMsg := azureDevOpsBases(c.Query("organisation_url"))
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return "", "", "", "", false
	}
	pat, ok = s.resolveAzureSecretParam(c, "personal_access_token", "Personal Access Token")
	if !ok {
		return "", "", "", "", false
	}
	return coreBase, releaseBase, pat, azureDevOpsAPIVersion(c.Query("api_version")), true
}

func (s *Service) getAzureDevOpsProjects(c *gin.Context) {
	coreBase, _, pat, version, ok := s.azureDevOpsResolve(c)
	if !ok {
		return
	}
	var parsed struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	// $top defaults to 100 and the ceiling is 1000; ask for the ceiling so a
	// large organisation's later projects still appear.
	endpoint := coreBase + "/_apis/projects?api-version=" + url.QueryEscape(version) + "&$top=1000&stateFilter=wellFormed"
	if !doAzureDevOpsGet(c, endpoint, pat, &parsed) {
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, p := range parsed.Value {
		if p.Name == "" {
			continue
		}
		// The NAME is the value, not the GUID: every path in the executor node
		// interpolates the project directly, and both are accepted there, but a
		// name is what the operator will recognise in a saved flow.
		options = append(options, api.InputOption{Name: p.Name, Value: p.Name})
	}
	writeAzureOptions(c, options)
}

func (s *Service) getAzureDevOpsRepositories(c *gin.Context) {
	coreBase, _, pat, version, ok := s.azureDevOpsResolve(c)
	if !ok {
		return
	}
	project, valid := azureDevOpsProject(c.Query("project"))
	if !valid {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load this list"})
		return
	}
	var parsed struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsDisabled bool   `json:"isDisabled"`
		} `json:"value"`
	}
	endpoint := coreBase + "/" + url.PathEscape(project) + "/_apis/git/repositories?api-version=" + url.QueryEscape(version)
	if !doAzureDevOpsGet(c, endpoint, pat, &parsed) {
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, r := range parsed.Value {
		if r.Name == "" || r.IsDisabled {
			continue
		}
		options = append(options, api.InputOption{Name: r.Name, Value: r.Name})
	}
	writeAzureOptions(c, options)
}

func (s *Service) getAzureDevOpsPipelines(c *gin.Context) {
	coreBase, _, pat, version, ok := s.azureDevOpsResolve(c)
	if !ok {
		return
	}
	project, valid := azureDevOpsProject(c.Query("project"))
	if !valid {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load this list"})
		return
	}
	var parsed struct {
		Value []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Folder string `json:"folder"`
		} `json:"value"`
	}
	endpoint := coreBase + "/" + url.PathEscape(project) + "/_apis/pipelines?api-version=" + url.QueryEscape(version) + "&$top=1000"
	if !doAzureDevOpsGet(c, endpoint, pat, &parsed) {
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, p := range parsed.Value {
		if p.ID == 0 || p.Name == "" {
			continue
		}
		// Pipelines are addressed by numeric id, so the value must be the id —
		// but a bare number is meaningless in a dropdown, so the folder is
		// carried into the label to disambiguate same-named pipelines in
		// different folders (Azure DevOps allows that; "\" is the root).
		label := p.Name
		if f := strings.Trim(p.Folder, `\`); f != "" {
			label = f + ` \ ` + p.Name
		}
		options = append(options, api.InputOption{Name: label, Value: strconv.Itoa(p.ID)})
	}
	writeAzureOptions(c, options)
}

func (s *Service) getAzureDevOpsReleaseDefinitions(c *gin.Context) {
	_, releaseBase, pat, version, ok := s.azureDevOpsResolve(c)
	if !ok {
		return
	}
	project, valid := azureDevOpsProject(c.Query("project"))
	if !valid {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load this list"})
		return
	}
	var parsed struct {
		Value []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"value"`
	}
	// Classic Release lives on vsrm, not dev.azure.com — the one place in this
	// node where the host differs.
	endpoint := releaseBase + "/" + url.PathEscape(project) + "/_apis/release/definitions?api-version=" + url.QueryEscape(version)
	if !doAzureDevOpsGet(c, endpoint, pat, &parsed) {
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, d := range parsed.Value {
		if d.ID == 0 || d.Name == "" {
			continue
		}
		label := d.Name
		if p := strings.Trim(d.Path, `\`); p != "" {
			label = p + ` \ ` + d.Name
		}
		options = append(options, api.InputOption{Name: label, Value: strconv.Itoa(d.ID)})
	}
	writeAzureOptions(c, options)
}

func (s *Service) getAzureDevOpsTeams(c *gin.Context) {
	coreBase, _, pat, version, ok := s.azureDevOpsResolve(c)
	if !ok {
		return
	}
	project, valid := azureDevOpsProject(c.Query("project"))
	if !valid {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Project to load this list"})
		return
	}
	var parsed struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	endpoint := coreBase + "/_apis/projects/" + url.PathEscape(project) + "/teams?api-version=" + url.QueryEscape(version) + "&$top=1000"
	if !doAzureDevOpsGet(c, endpoint, pat, &parsed) {
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, t := range parsed.Value {
		if t.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: t.Name, Value: t.Name})
	}
	writeAzureOptions(c, options)
}
