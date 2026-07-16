package http

// Live dropdowns for the Azure nodes: Storage containers, Cosmos DB databases
// and containers, Entra ID groups and users, Azure OpenAI deployments, and
// Azure AI Search indexes.
//
// Every proxy here follows the option-proxy conventions established by the
// Jenkins/Jira/WordPress files: the editor forwards the node's own auth inputs
// as query parameters (declared via the marker's Params in
// dynamicOptionsMetadata), secret-typed values arrive as ${secrets.X}
// references resolved server-side behind the EnvironmentView permission gate,
// and every failure is HTTP 200 + {"error": …} so the editor shows the message
// inline and falls back to manual entry.
//
// What is Azure-specific:
//
//   - Hosts are BUILT from caller-supplied names (account_name /
//     service_name / resource_name interpolated into *.core.windows.net /
//     *.search.windows.net / *.openai.azure.com hosts), and custom endpoints
//     (Azurite, the Cosmos emulator, sovereign clouds) are caller-supplied
//     URLs. The names are therefore pinned to ^[a-zA-Z0-9-]{1,90}$ before any
//     URL is assembled, custom endpoints must parse as http(s) URLs, and the
//     dial Control refuses link-local/cloud-metadata destinations even for
//     derived hosts (a DNS name resolving there is caught at the dial).
//     Loopback and private LAN stay allowed — the emulators live there.
//   - The Storage proxy implements the minimal SharedKey signer for its single
//     bodyless list GET (see azureStorageStringToSign); the Cosmos proxy signs
//     with the master key per the Cosmos HMAC scheme. Both are import-free
//     local copies — the executor's full signers are not shared across repos.
//   - The Entra-auth paths (storage/cosmos "entra" method, and the Graph
//     proxies) mint an app-only client-credentials token from
//     login.microsoftonline.com. azureLoginBase is a package-level seam so
//     tests can point the exchange at an httptest server.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"regexp"
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

// ---------------------------------------------------------------------------
// Dropdown registration
// ---------------------------------------------------------------------------

// The connection params the editor forwards on every fetch are the services'
// exact auth input names (the editor auto-appends "environment" to any marker
// with params, which is what lets a ${secrets.X} value resolve server-side).
var (
	azureStorageConnParams = []string{
		"account_name", "auth_method", "account_key",
		"azure_tenant_id", "azure_client_id", "azure_client_secret",
		"endpoint", "allow_insecure",
	}
	azureCosmosConnParams = []string{
		"account_name", "auth_method", "master_key",
		"azure_tenant_id", "azure_client_id", "azure_client_secret",
		"endpoint", "allow_insecure",
	}
	// The containers picker additionally needs the chosen database.
	azureCosmosContainerParams = append(append([]string{}, azureCosmosConnParams...), "database")
	azureEntraConnParams       = []string{
		"azure_tenant_id", "azure_client_id", "azure_client_secret", "graph_endpoint",
	}
	azureOpenAIConnParams   = []string{"api_key", "resource_name", "endpoint"}
	azureAISearchConnParams = []string{"service_name", "endpoint", "api_key", "api_version"}
)

// A picker belongs on an input that names an EXISTING resource. Inputs that
// name something being created (container_create's own `container`,
// database_create's `database`, index_create's `index_name`) deliberately keep
// a plain text field: offering a list of things that already exist to someone
// typing a new name is backwards, and the pgvector node sets the same
// precedent by excluding table_create. Parent references on a create action —
// cosmosdb/container_create's `database` — do get a picker, because that
// database must already exist.

// azureStorageContainerActions are the azure/storage actions with a
// `container` input naming an existing container. Absent: container_get_all
// and blob_find_by_tags (both account-scoped, no container input) and
// container_create (names a new container).
var azureStorageContainerActions = []string{
	"blob_copy", "blob_delete", "blob_download", "blob_lease", "container_lease",
	"blob_generate_sas", "blob_get_all", "blob_get_properties", "blob_get_tags",
	"blob_set_metadata", "blob_set_properties", "blob_set_tags", "blob_set_tier",
	"blob_snapshot", "blob_undelete", "blob_upload", "blob_upload_from_url",
	"container_delete", "container_get", "container_set_metadata",
}

// azureCosmosDatabaseActions are the azure/cosmosdb actions with a `database`
// input naming an existing database — every action except database_get_all
// (which lists databases) and database_create (which names a new one).
// container_create is included: its database must already exist.
var azureCosmosDatabaseActions = []string{
	"container_create", "container_delete", "container_get", "container_get_all",
	"container_replace", "database_delete", "database_get",
	"item_create", "item_delete", "item_get", "item_get_all", "item_patch",
	"item_query", "item_replace", "throughput_get", "throughput_update",
}

// azureCosmosContainerActions are the azure/cosmosdb actions with a `container`
// input naming an existing container (container_create names a new one).
var azureCosmosContainerActions = []string{
	"container_delete", "container_get", "container_replace",
	"item_create", "item_delete", "item_get", "item_get_all", "item_patch",
	"item_query", "item_replace", "throughput_get", "throughput_update",
}

// azureEntraUserPickerInputs lists, per azure/entra action, the inputs that
// select a user (they all resolve from the users proxy — including
// user_set_manager's manager_id).
var azureEntraUserPickerInputs = map[string][]string{
	"user_get":                    {"user_id"},
	"user_update":                 {"user_id"},
	"user_delete":                 {"user_id"},
	"user_add_to_group":           {"user_id"},
	"user_remove_from_group":      {"user_id"},
	"user_list_groups":            {"user_id"},
	"user_check_group_membership": {"user_id"},
	"user_assign_license":         {"user_id"},
	"user_revoke_sessions":        {"user_id"},
	"user_get_manager":            {"user_id"},
	"user_set_manager":            {"user_id", "manager_id"},
	"group_remove_member":         {"user_id"},
}

// azureEntraGroupPickerActions are the azure/entra actions with a `group_id`
// input. group_add_members' user_ids and user_check_group_membership's
// group_ids are comma-separated multi-value inputs, so they get no picker (the
// editor renders dynamic options as a single select).
var azureEntraGroupPickerActions = []string{
	"user_add_to_group", "user_remove_from_group",
	"group_get", "group_update", "group_delete", "group_list_members",
	"group_add_members", "group_remove_member", "group_list_owners",
}

// azureAISearchIndexActions are the vectordatabase/azureaisearch actions with
// an `index_name` input naming an existing index. Absent: index_get_all (it
// lists indexes) and index_create (its index_name is the new index's name —
// the endpoint is a create-or-update PUT, but the action is named and
// documented as a create, so it keeps a plain text field).
var azureAISearchIndexActions = []string{
	"document_count", "document_delete", "document_get", "document_upload",
	"index_delete", "index_get", "index_stats", "search",
}

// init registers the Azure live dropdowns into the shared
// dynamicOptionsMetadata map (declared in action.go). They are registered from
// tables here rather than spelled out as ~80 literal entries in action.go —
// the same approach as kubernetes_options.go and pgvector_options.go: the
// pattern is entirely regular, and the tables are checkable against the spec's
// action list at a glance. Package-level variables are initialised before any
// init() runs, so dynamicOptionsMetadata is non-nil at this point.
func init() {
	register := func(actionID, input, endpoint string, params []string) {
		dynamicOptionsMetadata[actionID+"#"+input] = api.InputDynamicOptions{
			Endpoint: "/api/v1/action/options/" + endpoint,
			Params:   params,
		}
	}

	for _, a := range azureStorageContainerActions {
		register("azure/storage/"+a, "container", "azure-storage-containers", azureStorageConnParams)
	}
	for _, a := range azureCosmosDatabaseActions {
		register("azure/cosmosdb/"+a, "database", "azure-cosmos-databases", azureCosmosConnParams)
	}
	for _, a := range azureCosmosContainerActions {
		register("azure/cosmosdb/"+a, "container", "azure-cosmos-containers", azureCosmosContainerParams)
	}
	for action, inputs := range azureEntraUserPickerInputs {
		for _, input := range inputs {
			register("azure/entra/"+action, input, "azure-entra-users", azureEntraConnParams)
		}
	}
	for _, a := range azureEntraGroupPickerActions {
		register("azure/entra/"+a, "group_id", "azure-entra-groups", azureEntraConnParams)
	}
	register("ai/azure_openai", "deployment", "azure-openai-deployments", azureOpenAIConnParams)
	for _, a := range azureAISearchIndexActions {
		register("vectordatabase/azureaisearch/"+a, "index_name", "azure-aisearch-indexes", azureAISearchConnParams)
	}
}

// ---------------------------------------------------------------------------
// Clients + hardening
// ---------------------------------------------------------------------------

// azureNamePattern pins the caller-supplied names that get interpolated into
// hosts (storage/cosmos account names, search service names, OpenAI resource
// names). Azure's own naming rules are stricter; this is the superset that is
// safe to place in a hostname — anything else is refused before a URL is
// built.
var azureNamePattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,90}$`)

// azureLoginBase is where the client-credentials token exchange goes. A
// package-level seam (not a constant) so tests can point it at an httptest
// server; production never changes it.
var azureLoginBase = "https://login.microsoftonline.com"

// azureOptionsDialControl blocks link-local + cloud-metadata destinations on
// the address actually dialed — custom endpoints are caller-supplied, and even
// derived *.azure.com hosts go through it so a poisoned DNS answer cannot
// reach the instance metadata service. Same SSRF hardening as the
// Jenkins/Jira/WordPress proxies; loopback and private LAN stay allowed for
// the Azurite and Cosmos emulators.
func azureOptionsDialControl(network, address string, _ syscall.RawConn) error {
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

// azureOptionsRedirect refuses cross-host redirects — a compromised custom
// endpoint must not be able to bounce a signed/authenticated request to
// another host.
func azureOptionsRedirect(req *gohttp.Request, via []*gohttp.Request) error {
	if len(via) >= 5 {
		return errors.New("stopped after too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return errors.New("cross-host redirect not allowed")
	}
	return nil
}

// azureOptionsHTTPClient / azureOptionsInsecureHTTPClient serve the dropdown
// proxies. Both are SSRF-hardened; the insecure one additionally skips TLS
// verification, used only when the node opted into Allow Insecure (the Cosmos
// emulator's self-signed certificate) — kept separate so the secure default
// can never be weakened.
var azureOptionsHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: azureOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: azureOptionsDialControl}).DialContext,
	},
}

var azureOptionsInsecureHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: azureOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext:     (&net.Dialer{Timeout: 5 * time.Second, Control: azureOptionsDialControl}).DialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — opt-in only
	},
}

// azureOptionsClient picks the client for the node's allow_insecure choice.
func azureOptionsClient(c *gin.Context) *gohttp.Client {
	if strings.EqualFold(strings.TrimSpace(c.Query("allow_insecure")), "true") {
		return azureOptionsInsecureHTTPClient
	}
	return azureOptionsHTTPClient
}

// azureOptionsBaseURL turns a user-supplied custom endpoint into a clean
// scheme+host[+path] base (no trailing slash, no userinfo), defaulting to
// https. Built via url.URL so a crafted endpoint can't smuggle userinfo or a
// query into the server-side request. The path is KEPT — Azurite endpoints
// carry the account as a path segment (http://host:10000/devstoreaccount1).
func azureOptionsBaseURL(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "${") {
		return "", errors.New("endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("endpoint must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("endpoint must be http or https")
	}
	u.User = nil
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// azureGraphVersionPath is the Graph version this proxy appends, matching the
// executor's graphAPIPath.
const azureGraphVersionPath = "/v1.0"

// azureGraphBaseURL normalises a graph_endpoint override exactly as the
// executor's entra normaliseEndpoint does: scheme+host[+path], no trailing
// slash, and a trailing /v1.0 stripped. The executor tolerates an endpoint
// written WITH the version suffix, so the same value must not double up here
// into {host}/v1.0/v1.0 — that 404s the dropdown while the action itself
// works.
func azureGraphBaseURL(raw string) (string, error) {
	base, err := azureOptionsBaseURL(raw)
	if err != nil {
		return "", err
	}
	// Trimming the whole base is equivalent to trimming the path the executor
	// trims: a host can never end in /v1.0, it carries no slash.
	return strings.TrimSuffix(base, azureGraphVersionPath), nil
}

// ---------------------------------------------------------------------------
// Shared parameter resolution
// ---------------------------------------------------------------------------

// resolveAzureSecretParam pulls one secret-typed query parameter, resolving a
// ${secrets.X} reference server-side behind the EnvironmentView permission
// gate (the plaintext never transits the browser). On any problem it writes
// the option-proxy error response (HTTP 200 + {"error": …}) and returns
// ok=false so the editor shows the message inline and falls back to manual
// entry.
func (s *Service) resolveAzureSecretParam(c *gin.Context, param, label string) (string, bool) {
	value := strings.TrimSpace(c.Query(param))
	// The canonical managed-credential reference is plural (${credentials.X});
	// the singular ${credential.X} is not an emitted format but is guarded
	// leniently so a mistyped/legacy reference still fails closed with a clear
	// message rather than being treated as a literal secret.
	if strings.HasPrefix(value, "${credentials.") || strings.HasPrefix(value, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the " + label + " (the flow itself still runs)"})
		return "", false
	}
	if strings.HasPrefix(value, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the " + label + " secret"})
			return "", false
		}
		// Resolving a secret to plaintext here must be gated by the same
		// permission as reading it through the environment endpoints: the
		// resolved value authenticates a request on the user's behalf, possibly
		// against a caller-supplied endpoint.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, value)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", false
		}
		value = resolved
	}
	if value == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the " + label + " to load this list"})
		return "", false
	}
	return value, true
}

// resolveAzureServicePrincipal pulls the service-principal triple every
// Entra-auth path uses (azure_tenant_id / azure_client_id plain,
// azure_client_secret a secret resolved from the environment). On any problem
// it writes the error response and returns ok=false.
func (s *Service) resolveAzureServicePrincipal(c *gin.Context) (tenantID, clientID, clientSecret string, ok bool) {
	tenantID = strings.TrimSpace(c.Query("azure_tenant_id"))
	if tenantID == "" || strings.HasPrefix(tenantID, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Tenant ID to load this list"})
		return "", "", "", false
	}
	clientID = strings.TrimSpace(c.Query("azure_client_id"))
	if clientID == "" || strings.HasPrefix(clientID, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Client ID to load this list"})
		return "", "", "", false
	}
	clientSecret, ok = s.resolveAzureSecretParam(c, "azure_client_secret", "Client Secret")
	if !ok {
		return "", "", "", false
	}
	return tenantID, clientID, clientSecret, true
}

// azureClientCredentialsToken mints an app-only OAuth2 client-credentials
// token from Microsoft Entra. Error strings are generic on purpose: the token
// endpoint's body can echo request material, and nothing from it (or the
// secret) may reach the client.
func azureClientCredentialsToken(c *gin.Context, tenantID, clientID, clientSecret, scope string) (string, string) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", scope)

	tokenURL := azureLoginBase + "/" + url.PathEscape(tenantID) + "/oauth2/v2.0/token"
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "Could not build the Microsoft Entra token request"
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := azureOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("azure options: unable to reach the Entra token endpoint")
		return "", "Could not reach Microsoft Entra to authenticate — check your connection and try again"
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "Failed to read the Microsoft Entra token response"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body carries AADSTS diagnostics that can include request material;
		// log the status only and keep the operator message generic.
		log.WithField("status", resp.StatusCode).Warn("azure options: Entra token exchange failed")
		return "", "Microsoft Entra rejected the credential exchange — check the Tenant ID, Client ID and Client Secret"
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return "", "Failed to parse the Microsoft Entra token response"
	}
	return parsed.AccessToken, ""
}

// doAzureOptionsGet executes one proxy request and returns the raw body,
// translating transport/HTTP errors into the friendly option-proxy message.
// provider names the upstream in operator-facing text ("Azure Storage",
// "Cosmos DB", …).
func doAzureOptionsGet(client *gohttp.Client, req *gohttp.Request, provider string) ([]byte, string) {
	resp, err := client.Do(req)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "provider": provider}).Warn("azure options: unable to reach the provider")
		return nil, "Could not reach " + provider + " — check the connection details and that the endpoint is reachable"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, provider + " rejected the request as unauthorised — check the credentials"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider + " returned an unexpected response (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Failed to read the " + provider + " response"
	}
	return body, ""
}

// writeAzureOptions sorts the options case-insensitively by name and writes
// the option-proxy success envelope.
func writeAzureOptions(c *gin.Context, options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// ---------------------------------------------------------------------------
// Azure Storage — SharedKey signing + the containers proxy
// ---------------------------------------------------------------------------

// azureStorageAPIVersion pins the proxy's list call to the same x-ms-version
// the executor actions send.
const azureStorageAPIVersion = "2023-11-03"

// azureStorageStringToSign builds the SharedKey string-to-sign for a bodyless
// GET, per the OFFICIAL header order (Content-Encoding before
// Content-Language — n8n has them swapped, which only works because both are
// empty): VERB, then the eleven standard header slots — Content-Encoding,
// Content-Language, Content-Length, Content-MD5, Content-Type, Date,
// If-Modified-Since, If-Match, If-None-Match, If-Unmodified-Since, Range —
// all empty here (Content-Length is "" when 0), then CanonicalizedHeaders and
// CanonicalizedResource.
//
// CanonicalizedHeaders sorts the x-ms-* header names byte-wise. The .NET
// culture-aware sort the full spec calls for differs from a byte sort only
// for exotic header names; this proxy emits exactly x-ms-date and
// x-ms-version (plain lowercase ASCII), so the edge cases don't arise.
//
// CanonicalizedResource is "/{account}{request path}" — for Azurite-style
// endpoints the account appears twice (once as the CanonicalizedResource
// account, once in the path), which is Microsoft's documented emulator rule —
// followed by the sorted, decoded query parameters as "name:value" lines
// (multi-values sorted and comma-joined).
func azureStorageStringToSign(account string, req *gohttp.Request) string {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteString(strings.Repeat("\n", 12))

	names := make([]string, 0, 2)
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ms-") {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString(n + ":" + strings.TrimSpace(req.Header.Get(n)) + "\n")
	}

	b.WriteString("/" + account + req.URL.Path)
	query := req.URL.Query()
	if len(query) > 0 {
		lower := make(map[string][]string, len(query))
		keys := make([]string, 0, len(query))
		for k, vs := range query {
			lk := strings.ToLower(k)
			if _, seen := lower[lk]; !seen {
				keys = append(keys, lk)
			}
			lower[lk] = append(lower[lk], vs...)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vs := lower[k]
			sort.Strings(vs)
			b.WriteString("\n" + k + ":" + strings.Join(vs, ","))
		}
	}
	return b.String()
}

// azureStorageSharedKeyAuth signs one request with the account key and
// returns the Authorization header value.
func azureStorageSharedKeyAuth(account, accountKeyB64 string, req *gohttp.Request) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(accountKeyB64))
	if err != nil || len(key) == 0 {
		return "", errors.New("account key is not valid base64")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(azureStorageStringToSign(account, req)))
	return "SharedKey " + account + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// getAzureStorageContainers serves the storage account's containers for every
// azure/storage Container picker. Auth mirrors the node: SharedKey (signed
// server-side with the account key) or a Microsoft Entra service principal.
func (s *Service) getAzureStorageContainers(c *gin.Context) {
	account := strings.TrimSpace(c.Query("account_name"))
	if !azureNamePattern.MatchString(account) {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Storage Account (letters, digits and dashes only) to load this list"})
		return
	}

	base := "https://" + account + ".blob.core.windows.net"
	if raw := strings.TrimSpace(c.Query("endpoint")); raw != "" && !strings.HasPrefix(raw, "${") {
		normalised, err := azureOptionsBaseURL(raw)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Custom Endpoint must be a full http(s) URL"})
			return
		}
		base = normalised
	}

	q := url.Values{}
	q.Set("comp", "list")
	q.Set("maxresults", "500")
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, base+"/?"+q.Encode(), nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Azure Storage request"})
		return
	}
	req.Header.Set("x-ms-date", time.Now().UTC().Format(gohttp.TimeFormat))
	req.Header.Set("x-ms-version", azureStorageAPIVersion)

	switch strings.ToLower(strings.TrimSpace(c.Query("auth_method"))) {
	case "", "shared_key":
		accountKey, ok := s.resolveAzureSecretParam(c, "account_key", "Account Key")
		if !ok {
			return
		}
		auth, err := azureStorageSharedKeyAuth(account, accountKey, req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Account Key is not valid base64 — copy it from the storage account's Access keys page"})
			return
		}
		req.Header.Set("Authorization", auth)
	case "entra":
		tenantID, clientID, clientSecret, ok := s.resolveAzureServicePrincipal(c)
		if !ok {
			return
		}
		token, errMsg := azureClientCredentialsToken(c, tenantID, clientID, clientSecret, "https://storage.azure.com/.default")
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
	default:
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Authentication method must be Shared Key or Microsoft Entra"})
		return
	}

	body, errMsg := doAzureOptionsGet(azureOptionsClient(c), req, "Azure Storage")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	var parsed struct {
		Containers struct {
			Container []struct {
				Name string `xml:"Name"`
			} `xml:"Container"`
		} `xml:"Containers"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Azure Storage response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Containers.Container))
	for _, container := range parsed.Containers.Container {
		if container.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: container.Name, Value: container.Name})
	}
	writeAzureOptions(c, options)
}

// ---------------------------------------------------------------------------
// Cosmos DB — master-key signing + the databases/containers proxies
// ---------------------------------------------------------------------------

// azureCosmosAPIVersion pins the proxy's calls to the same x-ms-version the
// executor actions send.
const azureCosmosAPIVersion = "2018-12-31"

// azureCosmosMasterKeyAuth signs one request per the Cosmos HMAC scheme —
// lowercased verb/resourceType/date over the base64-decoded master key, the
// payload ending with the empty second-date line — and returns the
// URL-encoded Authorization header value. resourceType/resourceID are passed
// explicitly by each caller (dbs list: type "dbs" id ""; colls list: type
// "colls" id "dbs/{db}").
func azureCosmosMasterKeyAuth(verb, resourceType, resourceID, date, masterKeyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(masterKeyB64))
	if err != nil || len(key) == 0 {
		return "", errors.New("master key is not valid base64")
	}
	payload := strings.ToLower(verb) + "\n" + strings.ToLower(resourceType) + "\n" + resourceID + "\n" + strings.ToLower(date) + "\n" + "\n"
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return url.QueryEscape("type=master&ver=1.0&sig=" + sig), nil
}

// azureCosmosOptionsGet resolves the Cosmos connection from the query
// parameters, authenticates one GET (master key or Entra), and returns the
// raw body. On any problem it writes the error response and returns ok=false.
func (s *Service) azureCosmosOptionsGet(c *gin.Context, resourceType, resourceID, path string) ([]byte, bool) {
	account := strings.TrimSpace(c.Query("account_name"))
	if !azureNamePattern.MatchString(account) {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Account Name (letters, digits and dashes only) to load this list"})
		return nil, false
	}

	base := "https://" + account + ".documents.azure.com"
	if raw := strings.TrimSpace(c.Query("endpoint")); raw != "" && !strings.HasPrefix(raw, "${") {
		normalised, err := azureOptionsBaseURL(raw)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Custom Endpoint must be a full http(s) URL"})
			return nil, false
		}
		base = normalised
	}

	date := time.Now().UTC().Format(gohttp.TimeFormat)
	var authValue string
	switch strings.ToLower(strings.TrimSpace(c.Query("auth_method"))) {
	case "", "master_key":
		masterKey, ok := s.resolveAzureSecretParam(c, "master_key", "Master Key")
		if !ok {
			return nil, false
		}
		auth, err := azureCosmosMasterKeyAuth(gohttp.MethodGet, resourceType, resourceID, date, masterKey)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Master Key is not valid base64 — copy it from the Cosmos account's Keys page"})
			return nil, false
		}
		authValue = auth
	case "entra":
		tenantID, clientID, clientSecret, ok := s.resolveAzureServicePrincipal(c)
		if !ok {
			return nil, false
		}
		// The AAD scope is the account endpoint's own host (derived, so custom
		// endpoints and sovereign clouds resolve the right audience).
		u, err := url.Parse(base)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Custom Endpoint must be a full http(s) URL"})
			return nil, false
		}
		token, errMsg := azureClientCredentialsToken(c, tenantID, clientID, clientSecret, u.Scheme+"://"+u.Hostname()+"/.default")
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return nil, false
		}
		authValue = url.QueryEscape("type=aad&ver=1.0&sig=" + token)
	default:
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Authentication method must be Master Key or Microsoft Entra"})
		return nil, false
	}

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, base+path, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Cosmos DB request"})
		return nil, false
	}
	req.Header.Set("Authorization", authValue)
	req.Header.Set("x-ms-date", date)
	req.Header.Set("x-ms-version", azureCosmosAPIVersion)
	req.Header.Set("Accept", "application/json")

	body, errMsg := doAzureOptionsGet(azureOptionsClient(c), req, "Cosmos DB")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return nil, false
	}
	return body, true
}

// getAzureCosmosDatabases serves the account's databases for every
// azure/cosmosdb Database picker.
func (s *Service) getAzureCosmosDatabases(c *gin.Context) {
	body, ok := s.azureCosmosOptionsGet(c, "dbs", "", "/dbs")
	if !ok {
		return
	}
	var parsed struct {
		Databases []struct {
			ID string `json:"id"`
		} `json:"Databases"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Cosmos DB response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Databases))
	for _, db := range parsed.Databases {
		if db.ID == "" {
			continue
		}
		options = append(options, api.InputOption{Name: db.ID, Value: db.ID})
	}
	writeAzureOptions(c, options)
}

// getAzureCosmosContainers serves the chosen database's containers for every
// azure/cosmosdb Container picker. Depends on the chosen database, forwarded
// as the "database" query param.
func (s *Service) getAzureCosmosContainers(c *gin.Context) {
	database := strings.TrimSpace(c.Query("database"))
	if database == "" || strings.HasPrefix(database, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Database to load its containers"})
		return
	}
	// The signature's resourceID is the RAW parent path; only the URL path is
	// segment-encoded.
	body, ok := s.azureCosmosOptionsGet(c, "colls", "dbs/"+database, "/dbs/"+url.PathEscape(database)+"/colls")
	if !ok {
		return
	}
	var parsed struct {
		DocumentCollections []struct {
			ID string `json:"id"`
		} `json:"DocumentCollections"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Cosmos DB response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.DocumentCollections))
	for _, coll := range parsed.DocumentCollections {
		if coll.ID == "" {
			continue
		}
		options = append(options, api.InputOption{Name: coll.ID, Value: coll.ID})
	}
	writeAzureOptions(c, options)
}

// ---------------------------------------------------------------------------
// Entra ID — the groups/users proxies (Microsoft Graph)
// ---------------------------------------------------------------------------

// azureGraphOptionsGet resolves the service principal, mints an app-only
// Graph token, and performs one GET against {graph_endpoint}/v1.0. The
// ConsistencyLevel/eventual + $count pair matches the executor's list calls
// (and is what makes $orderby work app-only). On any problem it writes the
// error response and returns ok=false.
func (s *Service) azureGraphOptionsGet(c *gin.Context, pathAndQuery string) ([]byte, bool) {
	tenantID, clientID, clientSecret, ok := s.resolveAzureServicePrincipal(c)
	if !ok {
		return nil, false
	}

	graphBase := "https://graph.microsoft.com"
	if raw := strings.TrimSpace(c.Query("graph_endpoint")); raw != "" && !strings.HasPrefix(raw, "${") {
		normalised, err := azureGraphBaseURL(raw)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Graph Endpoint must be a full http(s) URL"})
			return nil, false
		}
		graphBase = normalised
	}

	// The token audience is the Graph host itself, so sovereign-cloud endpoints
	// resolve their own audience rather than the public cloud's.
	u, err := url.Parse(graphBase)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Graph Endpoint must be a full http(s) URL"})
		return nil, false
	}
	token, errMsg := azureClientCredentialsToken(c, tenantID, clientID, clientSecret, u.Scheme+"://"+u.Hostname()+"/.default")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return nil, false
	}

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, graphBase+azureGraphVersionPath+pathAndQuery, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Microsoft Graph request"})
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ConsistencyLevel", "eventual")
	req.Header.Set("Accept", "application/json")

	body, errMsg := doAzureOptionsGet(azureOptionsHTTPClient, req, "Microsoft Graph")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return nil, false
	}
	return body, true
}

// azureGraphRow is the projection both directory pickers select.
type azureGraphRow struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// getAzureEntraGroups serves the tenant's first 100 groups by display name
// for every azure/entra group_id picker.
func (s *Service) getAzureEntraGroups(c *gin.Context) {
	q := url.Values{}
	q.Set("$select", "id,displayName")
	q.Set("$top", "100")
	q.Set("$orderby", "displayName")
	q.Set("$count", "true")
	body, ok := s.azureGraphOptionsGet(c, "/groups?"+q.Encode())
	if !ok {
		return
	}
	var parsed struct {
		Value []azureGraphRow `json:"value"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Microsoft Graph response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, row := range parsed.Value {
		if row.ID == "" {
			continue
		}
		name := strings.TrimSpace(row.DisplayName)
		if name == "" {
			name = row.ID
		}
		options = append(options, api.InputOption{Name: name, Value: row.ID})
	}
	writeAzureOptions(c, options)
}

// getAzureEntraUsers serves the tenant's first 100 users by display name as
// "Display Name (upn)" options for every azure/entra user_id / manager_id
// picker. Larger tenants fall back to typing an id or UPN (the manual-entry
// fallback).
func (s *Service) getAzureEntraUsers(c *gin.Context) {
	q := url.Values{}
	q.Set("$select", "id,displayName,userPrincipalName")
	q.Set("$top", "100")
	q.Set("$orderby", "displayName")
	q.Set("$count", "true")
	body, ok := s.azureGraphOptionsGet(c, "/users?"+q.Encode())
	if !ok {
		return
	}
	var parsed struct {
		Value []azureGraphRow `json:"value"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Microsoft Graph response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, row := range parsed.Value {
		if row.ID == "" {
			continue
		}
		name := strings.TrimSpace(row.DisplayName)
		if name == "" {
			name = row.ID
		}
		if upn := strings.TrimSpace(row.UserPrincipalName); upn != "" {
			name += " (" + upn + ")"
		}
		options = append(options, api.InputOption{Name: name, Value: row.ID})
	}
	writeAzureOptions(c, options)
}

// ---------------------------------------------------------------------------
// Azure OpenAI — the deployments proxy
// ---------------------------------------------------------------------------

// azureOpenAIDeploymentsAPIVersion is the data-plane api-version that carries
// the deployments listing (it only exists on this preview version — the chat
// api_version the node sends is NOT valid here, so it is deliberately not
// forwarded).
const azureOpenAIDeploymentsAPIVersion = "2023-03-15-preview"

// getAzureOpenAIDeployments serves the resource's deployments as
// "{id} ({model})" options for the ai/azure_openai Deployment picker.
func (s *Service) getAzureOpenAIDeployments(c *gin.Context) {
	base := ""
	if raw := strings.TrimSpace(c.Query("endpoint")); raw != "" && !strings.HasPrefix(raw, "${") {
		normalised, err := azureOptionsBaseURL(raw)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Custom Endpoint must be a full http(s) URL"})
			return
		}
		base = normalised
	} else {
		resource := strings.TrimSpace(c.Query("resource_name"))
		if resource == "" || strings.HasPrefix(resource, "${") {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Resource Name or Custom Endpoint to load this list"})
			return
		}
		if !azureNamePattern.MatchString(resource) {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Resource Name (letters, digits and dashes only) to load this list"})
			return
		}
		base = "https://" + resource + ".openai.azure.com"
	}

	apiKey, ok := s.resolveAzureSecretParam(c, "api_key", "API Key")
	if !ok {
		return
	}

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet,
		base+"/openai/deployments?api-version="+azureOpenAIDeploymentsAPIVersion, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Azure OpenAI request"})
		return
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	body, errMsg := doAzureOptionsGet(azureOptionsHTTPClient, req, "Azure OpenAI")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	var parsed struct {
		Data []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Azure OpenAI response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Data))
	for _, deployment := range parsed.Data {
		if deployment.ID == "" {
			continue
		}
		name := deployment.ID
		if deployment.Model != "" {
			name += " (" + deployment.Model + ")"
		}
		options = append(options, api.InputOption{Name: name, Value: deployment.ID})
	}
	writeAzureOptions(c, options)
}

// ---------------------------------------------------------------------------
// Azure AI Search — the indexes proxy
// ---------------------------------------------------------------------------

// azureAISearchDefaultAPIVersion matches the executor's default api_version.
const azureAISearchDefaultAPIVersion = "2024-07-01"

// getAzureAISearchIndexes serves the search service's indexes for every
// vectordatabase/azureaisearch Index picker.
func (s *Service) getAzureAISearchIndexes(c *gin.Context) {
	base := ""
	if raw := strings.TrimSpace(c.Query("endpoint")); raw != "" && !strings.HasPrefix(raw, "${") {
		normalised, err := azureOptionsBaseURL(raw)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Custom Endpoint must be a full http(s) URL"})
			return
		}
		base = normalised
	} else {
		service := strings.TrimSpace(c.Query("service_name"))
		if service == "" || strings.HasPrefix(service, "${") {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Service Name or Custom Endpoint to load this list"})
			return
		}
		if !azureNamePattern.MatchString(service) {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Service Name (letters, digits and dashes only) to load this list"})
			return
		}
		base = "https://" + service + ".search.windows.net"
	}

	apiKey, ok := s.resolveAzureSecretParam(c, "api_key", "API Key")
	if !ok {
		return
	}

	apiVersion := strings.TrimSpace(c.Query("api_version"))
	if apiVersion == "" || strings.HasPrefix(apiVersion, "${") {
		apiVersion = azureAISearchDefaultAPIVersion
	}

	q := url.Values{}
	q.Set("api-version", apiVersion)
	q.Set("$select", "name")
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, base+"/indexes?"+q.Encode(), nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Azure AI Search request"})
		return
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	body, errMsg := doAzureOptionsGet(azureOptionsClient(c), req, "Azure AI Search")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}

	var parsed struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Azure AI Search response"})
		return
	}
	options := make([]api.InputOption, 0, len(parsed.Value))
	for _, index := range parsed.Value {
		if index.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: index.Name, Value: index.Name})
	}
	writeAzureOptions(c, options)
}
