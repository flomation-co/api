package http

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// Live dropdowns for the Infrastructure ▸ AAP / AWX actions.
//
// Every picker (job templates, inventories, hosts, credentials, ad-hoc modules…)
// resolves through one of these proxies. There is no fixed upstream — the
// controller URL is per-node configuration — so the editor forwards the node's
// awx_url / auth_method / api_token / awx_username / awx_password /
// allow_insecure / api_prefix inputs as query parameters, declared via each
// marker's Params in dynamicOptionsMetadata. The token (or password) arrives as a
// ${secrets.X} reference and is resolved server-side, so the plaintext never
// transits the browser.
//
// Errors follow the option-proxy convention of HTTP 200 + {"error": …} so the
// editor renders the message inline under the field and falls back to manual
// entry. The only non-200 path is checkPermission writing 401/403 itself.

// ---------------------------------------------------------------------------
// Dropdown registration
// ---------------------------------------------------------------------------

// awxConnParams are the connection inputs the editor forwards on every AWX option
// fetch. `environment` is listed explicitly: the editor appends it to every
// dynamic-options request anyway, but declaring it is the newer convention (jira,
// trello, wordpress) and it is what lets the ${secrets.X} token be resolved
// server-side.
var awxConnParams = []string{
	"awx_url", "auth_method", "api_token", "awx_username", "awx_password",
	"allow_insecure", "api_prefix", "environment",
}

// awxInvScopedParams add the node's chosen inventory, so a picker lists only the
// hosts / groups / sources that actually live in it — the k8sNamespacedParams
// idiom applied to AWX's inventory scoping.
var awxInvScopedParams = append(append([]string{}, awxConnParams...), "inventory_id")

// awxOptionMarkers maps each dropdown endpoint to the "<action>#<input>" pairs it
// fills.
//
// This is a table rather than ~75 literal entries in dynamicOptionsMetadata
// because the pattern is entirely regular: every input naming an AWX object gets
// the picker for that object's collection. Action IDs are relative to
// infrastructure/awx/ unless they already carry a "trigger/" prefix.
//
// Note the deliberate splits. `credentials` is offered three ways — the full list
// for a job template's extra credentials, and kind-filtered lists for the two
// places AWX hard-rejects the wrong kind: an ad-hoc command demands a machine
// (ssh) credential, and a project demands a source-control (scm) one. Offering
// the full list there would guarantee a 400.
var awxOptionMarkers = map[string][]string{
	"awx-job-templates": {
		"job_template_launch#job_template_id",
		"job_template_get#job_template_id",
		"job_template_survey_get#job_template_id",
		"job_template_launch_options_get#job_template_id",
		"job_list#job_template_id",
		"schedule_list#job_template_id",
		"schedule_create#job_template_id",
		"trigger/awx_webhook#job_template_id",
	},
	"awx-workflow-templates": {
		"workflow_launch#workflow_template_id",
		"workflow_template_get#workflow_template_id",
		"trigger/awx_webhook#workflow_template_id",
	},
	"awx-inventories": {
		"job_template_launch#inventory_id",
		"workflow_launch#inventory_id",
		"adhoc_command_run#inventory_id",
		"schedule_create#inventory_id",
		"inventory_get#inventory_id",
		"inventory_update#inventory_id",
		"inventory_delete#inventory_id",
		"host_list#inventory_id",
		"host_get#inventory_id",
		"host_create#inventory_id",
		"host_update#inventory_id",
		"host_delete#inventory_id",
		"host_group_assign#inventory_id",
		"group_list#inventory_id",
		"group_get#inventory_id",
		"group_create#inventory_id",
		"group_update#inventory_id",
		"group_delete#inventory_id",
		"inventory_source_list#inventory_id",
		"inventory_source_get#inventory_id",
		"inventory_source_sync#inventory_id",
	},
	"awx-groups": {
		"group_get#group_id",
		"group_update#group_id",
		"group_delete#group_id",
		"host_group_assign#group_id",
		"host_list#group_id",
	},
	"awx-hosts": {
		"host_get#host_id",
		"host_update#host_id",
		"host_delete#host_id",
		"host_group_assign#host_id",
	},
	"awx-inventory-sources": {
		"inventory_source_get#inventory_source_id",
		"inventory_source_sync#inventory_source_id",
	},
	"awx-projects": {
		"project_sync#project_id",
		"project_get#project_id",
		"project_update#project_id",
		"project_delete#project_id",
		"job_template_list#project_id",
	},
	"awx-credentials": {
		"job_template_launch#credentials",
		"credential_get#credential_id",
		"credential_delete#credential_id",
	},
	"awx-machine-credentials": {
		"adhoc_command_run#credential_id",
	},
	"awx-scm-credentials": {
		"project_create#credential_id",
		"project_update#credential_id",
	},
	"awx-credential-types": {
		"credential_create#credential_type_id",
		"credential_list#credential_type_id",
	},
	"awx-organizations": {
		"inventory_create#organization_id",
		"project_create#organization_id",
		"credential_create#organization_id",
		"project_list#organization_id",
		"inventory_list#organization_id",
		"credential_list#organization_id",
		"workflow_template_list#organization_id",
		"team_list#organization_id",
		"user_list#organization_id",
		"execution_environment_list#organization_id",
	},
	"awx-execution-environments": {
		"job_template_launch#execution_environment_id",
		"adhoc_command_run#execution_environment_id",
		"project_create#default_environment_id",
	},
	"awx-labels": {
		"job_template_launch#labels",
		"workflow_launch#labels",
	},
	"awx-instance-groups": {
		"job_template_launch#instance_groups",
	},
	"awx-schedules": {
		"schedule_update#schedule_id",
		"schedule_delete#schedule_id",
	},
	"awx-adhoc-modules": {
		"adhoc_command_run#module_name",
	},
}

// init registers the AWX live dropdowns into the shared dynamicOptionsMetadata
// map. Package-level variables are initialised before any init() runs, so both
// dynamicOptionsMetadata and awxOptionResources are non-nil here.
func init() {
	for endpoint, markers := range awxOptionMarkers {
		// An inventory-scoped picker must also forward the node's inventory_id, or
		// it would list every host in the controller instead of the ones in the
		// inventory the operator picked.
		params := awxConnParams
		if awxOptionResources[strings.TrimPrefix(endpoint, "awx-")].InventoryScoped {
			params = awxInvScopedParams
		}

		for _, marker := range markers {
			actionID, input, ok := strings.Cut(marker, "#")
			if !ok || actionID == "" || input == "" {
				panic("awx_options: malformed dropdown marker " + marker)
			}
			if !strings.HasPrefix(actionID, "trigger/") {
				actionID = "infrastructure/awx/" + actionID
			}
			dynamicOptionsMetadata[actionID+"#"+input] = api.InputDynamicOptions{
				Endpoint: "/api/v1/action/options/" + endpoint,
				Params:   params,
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The pickers
// ---------------------------------------------------------------------------

// awxLabelStyle picks how a row is rendered in the dropdown. AWX object names are
// only unique within an organization (or an inventory), so several pickers must
// qualify the name or the operator sees two identical entries and cannot tell
// which is which.
type awxLabelStyle int

const (
	awxLabelPlain awxLabelStyle = iota
	// awxLabelOrg renders "Demo Job Template (Default)".
	awxLabelOrg
	// awxLabelCredentialType renders "Deploy Key (Source Control)" so an SSH key
	// is distinguishable from a cloud key at a glance.
	awxLabelCredentialType
	// awxLabelSchedule renders "Nightly — Demo Job Template (next run: …)".
	awxLabelSchedule
)

// awxOptionResource locates one AWX API v2 collection. It is the AWX analogue of
// k8sOptionResource: one generic handler, parameterised by the collection, serves
// every picker whose only difference is which list it reads.
type awxOptionResource struct {
	// Collection is the API v2 collection name, appended to the resolved API root.
	Collection string
	// Filter is a fixed query applied to every request (AWX's field lookups).
	Filter url.Values
	// InventoryScoped lists only the objects inside the node's chosen inventory.
	InventoryScoped bool
	// Label picks the display form.
	Label awxLabelStyle
	// What names the objects in operator-facing error messages.
	What string
}

// awxOptionResources is the set of collections that back a live dropdown, keyed by
// the slug in the route (/action/options/awx-<slug>). The ad-hoc module list is
// deliberately absent — it is a settings key, not a collection, and has its own
// handler.
var awxOptionResources = map[string]awxOptionResource{
	"job-templates":      {Collection: "job_templates", Label: awxLabelOrg, What: "job templates"},
	"workflow-templates": {Collection: "workflow_job_templates", Label: awxLabelOrg, What: "workflow templates"},
	"inventories":        {Collection: "inventories", Label: awxLabelOrg, What: "inventories"},
	"projects":           {Collection: "projects", Label: awxLabelOrg, What: "projects"},
	"organizations":      {Collection: "organizations", What: "organizations"},
	"credentials":        {Collection: "credentials", Label: awxLabelCredentialType, What: "credentials"},
	// AWX hard-rejects a non-machine credential on an ad-hoc command
	// (400 "You must provide a machine / SSH credential."), and a non-scm one on a
	// project — so those two inputs get kind-filtered lists rather than the full one.
	"machine-credentials":    {Collection: "credentials", Filter: url.Values{"credential_type__kind": {"ssh"}}, Label: awxLabelCredentialType, What: "machine credentials"},
	"scm-credentials":        {Collection: "credentials", Filter: url.Values{"credential_type__kind": {"scm"}}, Label: awxLabelCredentialType, What: "source-control credentials"},
	"credential-types":       {Collection: "credential_types", What: "credential types"},
	"execution-environments": {Collection: "execution_environments", What: "execution environments"},
	"labels":                 {Collection: "labels", What: "labels"},
	"instance-groups":        {Collection: "instance_groups", What: "instance groups"},
	"schedules":              {Collection: "schedules", Label: awxLabelSchedule, What: "schedules"},

	// Inventory-scoped: AWX namespaces these under an inventory, and a controller
	// with several inventories would otherwise show every host in every one.
	"groups":            {Collection: "groups", InventoryScoped: true, What: "groups"},
	"hosts":             {Collection: "hosts", InventoryScoped: true, What: "hosts"},
	"inventory-sources": {Collection: "inventory_sources", InventoryScoped: true, What: "inventory sources"},
}

// awxOptionRouteSlugs is the registration order for the generic pickers. It is the
// single source of truth shared by service.go's route loop and the guard test, so
// a new resource cannot be added to awxOptionResources without a route (which
// would be a silent 404 → the dropdown falls back to manual entry with no error).
var awxOptionRouteSlugs = []string{
	"job-templates", "workflow-templates", "inventories", "groups", "hosts",
	"inventory-sources", "projects", "organizations", "credentials",
	"machine-credentials", "scm-credentials", "credential-types",
	"execution-environments", "labels", "instance-groups", "schedules",
}

// ---------------------------------------------------------------------------
// API-root discovery
// ---------------------------------------------------------------------------

// awxAPIRoots are the two roots an AWX / AAP controller serves its API under.
//
// Upstream AWX exposes /api/v2/. AAP 2.5+ puts the controller behind a platform
// gateway and moves it to /api/controller/v2/. There is no free way to tell them
// apart, so the first request sweeps: a 404 means "not this shape, try the next
// root"; ANY other status is conclusive and stops the sweep. That distinction
// matters — a 401 must NOT sweep, or a merely-wrong token would be reported as
// "there is no AWX at this URL", sending the operator to debug the wrong thing.
//
// This deliberately duplicates the executor's fuller ResolveAPIRoot
// (executor/actions/infrastructure/awx/common.go): the api cannot import the
// executor module. The executor's version additionally probes /api/ and keys off
// the ABSENCE of available_versions; a dropdown proxy does not need that extra
// round trip, since it is about to make a real request anyway and can just read
// its status. Keep the two in sync — and note the api_prefix override is the
// escape hatch if a deployment serves the API somewhere else entirely.
var awxAPIRoots = []string{"/api/v2/", "/api/controller/v2/"}

const (
	// awxOptionPageSize is AWX's maximum page size.
	awxOptionPageSize = "200"

	// maxAWXOptionPages bounds the pagination walk at 10 × 200 = 2000 options. A
	// controller with more objects than that in one collection is not usefully
	// browsed from a select box; the operator types the ID.
	maxAWXOptionPages = 10

	awxRootNotFoundMsg = "Could not find the AWX / AAP API at that URL — check the URL, and set the API Prefix if the controller sits behind a gateway"
	awxNotJSONMsg      = "That URL did not answer like an AWX / AAP controller — check the AWX / AAP URL"
	awxUnreachableMsg  = "Could not reach AWX — check the AWX / AAP URL and that the controller is running"
	awxUnauthorisedMsg = "AWX rejected the credentials — check the API Token (or the Username and Password)"
)

// ---------------------------------------------------------------------------
// SSRF-hardened clients
// ---------------------------------------------------------------------------

// awxOptionsDialControl refuses link-local and cloud-metadata destinations. It
// runs on the address actually dialled — including redirect targets — so a DNS
// name or a rebind resolving to one of them is caught too. Loopback and private
// LAN ranges stay allowed: a self-hosted AWX almost always lives there, exactly as
// with the Jenkins, WordPress and Kubernetes proxies. isCloudMetadataIP and
// blockedMetadataIPs are shared, declared in jenkins_options.go.
func awxOptionsDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		// Covers 169.254.169.254 and its ::ffff: mapped form.
		return errors.New("link-local addresses are not allowed")
	}
	if isCloudMetadataIP(ip) {
		return errors.New("cloud metadata addresses are not allowed")
	}
	return nil
}

// awxOptionsRedirect refuses cross-host redirects. The dial Control blocks
// metadata IPs even mid-redirect, but a redirect to a *different* host in an
// allowed (private) range would still be followed — and AWX answers its own API
// directly, so a hop off the host is never legitimate.
func awxOptionsRedirect(req *gohttp.Request, via []*gohttp.Request) error {
	if len(via) >= 5 {
		return errors.New("stopped after too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return errors.New("cross-host redirect not allowed")
	}
	return nil
}

// awxOptionsHTTPClient and awxOptionsInsecureHTTPClient serve the dropdown
// proxies. Both are SSRF-hardened; the insecure one additionally skips TLS
// verification, and is used only when the node opted into "Allow Insecure TLS"
// (self-signed AWX is common on a private network). They are kept as two separate
// clients — never one client whose TLS config is mutated per request — so the
// secure default cannot be weakened by a stray request.
var awxOptionsHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: awxOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, Control: awxOptionsDialControl}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	},
}

var awxOptionsInsecureHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: awxOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: awxOptionsDialControl}).DialContext,
		// #nosec G402 -- InsecureSkipVerify is an explicit per-node opt-in
		// (allow_insecure) for self-signed controllers, never the default.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	},
}

func awxOptionsClient(insecure bool) *gohttp.Client {
	if insecure {
		return awxOptionsInsecureHTTPClient
	}
	return awxOptionsHTTPClient
}

// ---------------------------------------------------------------------------
// Connection resolution
// ---------------------------------------------------------------------------

type awxProxyConn struct {
	// Base is scheme://host[/context-path], with no trailing slash.
	Base string
	// Auth is the complete Authorization header value ("Bearer …" or "Basic …").
	Auth string
	// Prefix, when set, is an operator-supplied API root override ("/api/v2/") and
	// replaces the two-candidate sweep entirely.
	Prefix   string
	Insecure bool
}

// resolveAWXConn reads the node's connection out of the query parameters,
// resolving the token (or password) secret server-side.
//
// The returned message, when non-empty, is the operator-facing text to render in
// place of the dropdown; the caller must stop. An empty message with ok==false
// means the response was already written (checkPermission's 401/403).
func (s *Service) resolveAWXConn(c *gin.Context) (awxProxyConn, string, bool) {
	base, err := awxOptionsBaseURL(strings.TrimSpace(c.Query("awx_url")))
	if err != nil {
		// Also the path taken when awx_url holds an unresolved ${...} reference.
		return awxProxyConn{}, "Set the AWX / AAP URL (a full http(s) URL) to load this list", false
	}

	var authHeader string
	if strings.EqualFold(strings.TrimSpace(c.Query("auth_method")), "basic") {
		// Basic auth is the fallback method: it is disabled outright on some
		// controllers (AUTH_BASIC_ENABLED=false) and never works for SSO users.
		username := strings.TrimSpace(c.Query("awx_username"))
		if username == "" || strings.HasPrefix(username, "${") {
			return awxProxyConn{}, "Set the Username to load this list", false
		}
		password, msg, ok := s.resolveAWXSecret(c, c.Query("awx_password"), "Password")
		if !ok {
			return awxProxyConn{}, msg, false
		}
		if password == "" {
			return awxProxyConn{}, "Set the Password to load this list", false
		}
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	} else {
		token, msg, ok := s.resolveAWXSecret(c, c.Query("api_token"), "API Token")
		if !ok {
			return awxProxyConn{}, msg, false
		}
		if token == "" {
			return awxProxyConn{}, "Set the API Token to load this list", false
		}
		authHeader = "Bearer " + token
	}

	return awxProxyConn{
		Base:     base,
		Auth:     authHeader,
		Prefix:   awxNormalisePrefix(c.Query("api_prefix")),
		Insecure: strings.EqualFold(strings.TrimSpace(c.Query("allow_insecure")), "true"),
	}, "", true
}

// resolveAWXSecret turns one credential query parameter into a plaintext value.
//
// A ${secrets.X} reference is resolved server-side — the plaintext never transits
// the browser — and that resolution is gated by the same permission as reading the
// secret through the environment endpoints. That gate is a security invariant, not
// a nicety: the resolved value authenticates a request to a CALLER-SUPPLIED host,
// so without it a member denied environment.view could exfiltrate any secret in
// the environment by aiming this proxy at a server they control.
//
// ok==false with an empty message means checkPermission already wrote the response.
func (s *Service) resolveAWXSecret(c *gin.Context, raw, label string) (string, string, bool) {
	value := strings.TrimSpace(raw)

	if strings.HasPrefix(value, "${credentials.") || strings.HasPrefix(value, "${credential.") {
		return "", "Managed credentials can't be used to load this list — use an environment secret for the " + label + " (the flow itself still runs)", false
	}

	if strings.HasPrefix(value, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			return "", "Select an environment to resolve the " + label + " secret", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, value)
		if errMsg != "" {
			return "", errMsg, false
		}
		return resolved, "", true
	}

	return value, "", true
}

// awxOptionsBaseURL turns a user-supplied controller URL into a clean
// scheme://host[/context-path] base with no trailing slash, defaulting to https.
//
// It is built through url.URL rather than string concatenation so a crafted base
// (a trailing "?", a fragment, embedded userinfo) can neither smuggle credentials
// into the server-side request nor displace the API path we append. A base that
// already ends in an API root is trimmed, because operators paste the URL they see
// — otherwise we would build /api/v2/api/v2/job_templates/.
func awxOptionsBaseURL(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "${") {
		return "", errors.New("awx_url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("awx_url must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("awx_url must be http or https")
	}

	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""

	path := strings.TrimRight(u.Path, "/")
	// Longest first: "/api/controller/v2" must not be half-matched by "/api".
	for _, suffix := range []string{"/api/controller/v2", "/api/v2", "/api"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	u.Path = path
	u.RawPath = ""

	return strings.TrimRight(u.String(), "/"), nil
}

// awxNormalisePrefix accepts the operator's api_prefix override ("api/v2",
// "/api/controller/v2/", …) and returns it as "/…/". An unset, unresolved or
// traversal-bearing value returns "", which falls back to the two-candidate sweep.
func awxNormalisePrefix(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" || strings.HasPrefix(p, "${") || strings.Contains(p, "..") {
		return ""
	}
	// Tolerate a whole URL being pasted into the prefix field.
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil && u.Path != "" {
			p = u.Path
		}
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// ---------------------------------------------------------------------------
// Fetching
// ---------------------------------------------------------------------------

// awxRow is the slice of an AWX list result a dropdown needs: the primary key, the
// display name, and the few summary fields used to qualify identically named
// objects. AWX ids are JSON numbers, so the option Value is the id stringified.
type awxRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	NextRun string `json:"next_run"` // schedules only; null on a disabled schedule
	Summary struct {
		Organization struct {
			Name string `json:"name"`
		} `json:"organization"`
		CredentialType struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"credential_type"`
		UnifiedJobTemplate struct {
			Name string `json:"name"`
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
}

// awxPage is AWX's pagination envelope. `next` is a path relative to the instance
// root ("/api/v2/job_templates/?page=2"), not an absolute URL.
type awxPage struct {
	Next    *string  `json:"next"`
	Results []awxRow `json:"results"`
}

// awxRequestURL joins base + API root + collection, always with the trailing slash
// AWX's Django APPEND_SLASH demands.
func awxRequestURL(base, root, collection string, query url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + root + collection + "/"
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// awxNextURL resolves AWX's pagination `next` link against the page we just read,
// then pins scheme and host back to the operator's base URL. AWX emits `next`
// relative, so an absolute one pointing anywhere else is either a misconfigured
// instance or an attempt to walk the proxy off-host — either way it must not move
// the fetch to a host the operator never named.
func awxNextURL(base, current, next string) (string, error) {
	n, err := url.Parse(strings.TrimSpace(next))
	if err != nil {
		return "", err
	}
	cur, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	resolved := cur.ResolveReference(n)
	resolved.Scheme = b.Scheme
	resolved.Host = b.Host
	resolved.User = nil
	resolved.Fragment = ""
	return resolved.String(), nil
}

// awxGet performs one authenticated GET. It returns (body, "", false) on success,
// (nil, message, false) on a conclusive failure, and (nil, "", true) on a 404 —
// which the root sweep reads as "not this API shape, try the next root".
func awxGet(c *gin.Context, client *gohttp.Client, auth, target, what string) ([]byte, string, bool) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, target, nil)
	if err != nil {
		return nil, "Could not build the request to AWX", false
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach AWX for options")
		return nil, awxUnreachableMsg, false
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Could not read the response from AWX", false
	}

	switch {
	case resp.StatusCode == gohttp.StatusNotFound:
		return nil, "", true
	case resp.StatusCode == gohttp.StatusUnauthorized:
		return nil, awxUnauthorisedMsg, false
	case resp.StatusCode == gohttp.StatusForbidden:
		// AWX 403s a token that authenticates but lacks read access on the
		// collection. Saying "check your token" here would be actively misleading.
		return nil, fmt.Sprintf("The AWX user is not allowed to list %s — grant it read access, or type the ID manually", what), false
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Sprintf("AWX returned an unexpected response (HTTP %d)", resp.StatusCode), false
	}

	return body, "", false
}

// awxSweep issues one GET per candidate API root until a root answers, returning
// the body and the URL that worked (the pagination walk resolves `next` against
// it). Only a 404 advances the sweep; every other outcome is conclusive.
func awxSweep(c *gin.Context, conn awxProxyConn, collection string, query url.Values, what string) ([]byte, string, string) {
	roots := awxAPIRoots
	if conn.Prefix != "" {
		roots = []string{conn.Prefix}
	}
	client := awxOptionsClient(conn.Insecure)

	for i, root := range roots {
		target, err := awxRequestURL(conn.Base, root, collection, query)
		if err != nil {
			return nil, "", "The AWX / AAP URL is not valid"
		}

		body, errMsg, notFound := awxGet(c, client, conn.Auth, target, what)
		switch {
		case notFound && i < len(roots)-1:
			continue // not upstream AWX — try the AAP gateway root
		case notFound:
			return nil, "", awxRootNotFoundMsg
		case errMsg != "":
			return nil, "", errMsg
		}
		return body, target, ""
	}
	return nil, "", awxRootNotFoundMsg
}

// fetchAWXList reads one collection, following AWX's pagination so an instance
// with more than a page of job templates still fills its dropdown.
func fetchAWXList(c *gin.Context, conn awxProxyConn, res awxOptionResource, query url.Values) ([]awxRow, string) {
	q := url.Values{}
	for k, values := range res.Filter {
		for _, v := range values {
			q.Add(k, v)
		}
	}
	for k, values := range query {
		for _, v := range values {
			q.Add(k, v)
		}
	}
	q.Set("page_size", awxOptionPageSize)
	q.Set("order_by", "name")

	body, current, errMsg := awxSweep(c, conn, res.Collection, q, res.What)
	if errMsg != "" {
		return nil, errMsg
	}

	var page awxPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, awxNotJSONMsg
	}
	rows := page.Results

	client := awxOptionsClient(conn.Insecure)
	for n := 1; n < maxAWXOptionPages && page.Next != nil && *page.Next != ""; n++ {
		target, err := awxNextURL(conn.Base, current, *page.Next)
		if err != nil {
			break
		}
		nextBody, errMsg, _ := awxGet(c, client, conn.Auth, target, res.What)
		if errMsg != "" {
			// A partial list beats no list: the operator can still pick from what
			// loaded, or type the ID.
			log.WithFields(log.Fields{"collection": res.Collection, "error": errMsg}).
				Warn("AWX option pagination stopped early")
			break
		}
		var next awxPage
		if err := json.Unmarshal(nextBody, &next); err != nil {
			break
		}
		rows = append(rows, next.Results...)
		current = target
		page = next
	}

	return rows, ""
}

func awxOptionLabel(r awxRow, style awxLabelStyle) string {
	switch style {
	case awxLabelOrg:
		if org := r.Summary.Organization.Name; org != "" {
			return r.Name + " (" + org + ")"
		}
	case awxLabelCredentialType:
		// The human name ("Machine", "Source Control") reads better than the raw
		// kind ("ssh", "scm"); fall back to the kind on an older serializer.
		if kind := r.Summary.CredentialType.Name; kind != "" {
			return r.Name + " (" + kind + ")"
		}
		if kind := r.Summary.CredentialType.Kind; kind != "" {
			return r.Name + " (" + kind + ")"
		}
	case awxLabelSchedule:
		label := r.Name
		if jt := r.Summary.UnifiedJobTemplate.Name; jt != "" {
			label += " — " + jt
		}
		if r.NextRun != "" {
			label += " (next run: " + r.NextRun + ")"
		}
		return label
	case awxLabelPlain:
	}
	return r.Name
}

func awxRowsToOptions(rows []awxRow, style awxLabelStyle) []api.InputOption {
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.Name == "" || r.ID == 0 {
			continue
		}
		options = append(options, api.InputOption{
			Name:  awxOptionLabel(r, style),
			Value: strconv.FormatInt(r.ID, 10),
		})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// awxOptions serves one AWX collection as dropdown options. One handler backs all
// sixteen table-driven pickers; only the ad-hoc module list needs its own.
func (s *Service) awxOptions(slug string) gin.HandlerFunc {
	res, known := awxOptionResources[slug]
	if !known {
		// Fail at route-registration time (i.e. on boot) rather than degrading a
		// dropdown at runtime — the same contract as kubernetesOptions.
		panic("awxOptions: unknown resource slug " + slug)
	}

	return func(c *gin.Context) {
		conn, errMsg, ok := s.resolveAWXConn(c)
		if !ok {
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			}
			return
		}

		query := url.Values{}
		if res.InventoryScoped {
			inventory := strings.TrimSpace(c.Query("inventory_id"))
			if inventory == "" || strings.HasPrefix(inventory, "${") {
				c.JSON(gohttp.StatusOK, gin.H{"error": "Choose an Inventory first to load its " + res.What})
				return
			}
			// AWX filters by primary key. Sending a name would be a 400 from the
			// controller; catching it here gives the operator an answer instead.
			if _, err := strconv.ParseInt(inventory, 10, 64); err != nil {
				c.JSON(gohttp.StatusOK, gin.H{"error": "Pick the Inventory from its list (AWX needs the numeric ID) to load its " + res.What})
				return
			}
			query.Set("inventory", inventory)
		}

		rows, errMsg := fetchAWXList(c, conn, res, query)
		if errMsg != "" {
			log.WithFields(log.Fields{"collection": res.Collection, "error": errMsg}).Warn("unable to fetch AWX options")
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		c.JSON(gohttp.StatusOK, gin.H{"options": awxRowsToOptions(rows, res.Label)})
	}
}

// getAWXAdHocModules lists the Ansible modules the controller allows in an ad-hoc
// command, for adhoc_command_run's Module input.
//
// This is the one picker that is not a collection. The allow-list is an
// admin-editable runtime setting (Settings ▸ Jobs ▸ "Ansible Modules Allowed for
// Ad Hoc Jobs") returned as a bare JSON array under AD_HOC_COMMANDS, and it MUST
// be read live: AWX hard-rejects any module outside the list, so a hardcoded list
// is wrong in both directions on a customised instance — it offers modules the
// controller will refuse, and hides modules the admin added. Short names only
// ("shell", never "ansible.builtin.shell"), which is what AWX stores.
func (s *Service) getAWXAdHocModules(c *gin.Context) {
	conn, errMsg, ok := s.resolveAWXConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	body, _, errMsg := awxSweep(c, conn, "settings/jobs", url.Values{}, "job settings")
	if errMsg != "" {
		log.WithField("error", errMsg).Warn("unable to fetch AWX ad-hoc modules")
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	var settings struct {
		AdHocCommands []string `json:"AD_HOC_COMMANDS"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": awxNotJSONMsg})
		return
	}

	options := make([]api.InputOption, 0, len(settings.AdHocCommands))
	for _, module := range settings.AdHocCommands {
		module = strings.TrimSpace(module)
		if module == "" {
			continue
		}
		options = append(options, api.InputOption{Name: module, Value: module})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}
