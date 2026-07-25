package http

// Live dropdowns for the CRM ▸ Salesforce actions.
//
// Salesforce is the node where pickers matter most. Every other CRM input is a
// name or a date; Salesforce inputs are RECORD IDS (0015f00000AbCdEAAV) and
// PICKLIST API NAMES that must match the org's setup exactly. A receptionist or
// sales admin — the person these flows are built for — cannot be asked to go and
// look either of those up. Eleven proxies back all 429 markers registered from
// salesforce_options_markers.go:
//
//	/salesforce-objects                 the global describe (all / custom only)
//	/salesforce-fields                  one object's fields (all|createable|updateable|picklist)
//	/salesforce-picklist                one field's picklist values — EVERY picklist input
//	/salesforce-external-id-fields      fields usable as an upsert key
//	/salesforce-record-types            one object's active record types
//	/salesforce-lookup                  searchable record picker (one or more objects)
//	/salesforce-users                   active users
//	/salesforce-owners                  users AND queues for one object
//	/salesforce-campaign-member-status  one campaign's member statuses (two-hop)
//	/salesforce-list-views              one object's list views
//	/salesforce-reports                 reports, or the distinct report folders
//
// ─────────────────────────────────────────────────────────────────────────────
// THE FOUR THINGS THIS FILE DOES THAT THE EQUIVALENT n8n NODE GETS WRONG
// ─────────────────────────────────────────────────────────────────────────────
//
//  1. It never pages a whole table. n8n's record pickers pull every Account /
//     User / Campaign page by page on each dropdown open — multi-second on a real
//     org and a needless load on the customer's API limits. /salesforce-lookup
//     issues ONE server-side-filtered, ORDER BY'd, LIMIT 100 SOQL query.
//
//  2. It filters picklist values on active == true. n8n returns every entry
//     describe hands back, including values retired in Setup, so the dropdown
//     offers choices Salesforce then rejects on write.
//
//  3. /salesforce-users defaults to IsActive = true. n8n lists deactivated users;
//     assigning one comes back as INVALID_CROSS_REFERENCE_KEY, which reads to the
//     operator like the integration is broken.
//
//  4. /salesforce-owners merges queues with users and prefixes BOTH groups
//     unconditionally. n8n prefixes users only when a queue happens to exist, so
//     the same field is labelled differently in two orgs. Lead.OwnerId and
//     Case.OwnerId legitimately take a queue id, so the queues have to be there.
//
// ─────────────────────────────────────────────────────────────────────────────
// SECURITY
// ─────────────────────────────────────────────────────────────────────────────
//
//   - instance_url is attacker-influencable and becomes the request host. It is
//     normalised and checked against Salesforce-owned suffixes — requiring a real
//     subdomain, so "salesforce.com" and "evilsalesforce.com" are both refused —
//     BEFORE the token is attached. Same guard, same reason, as the executor's
//     salesforce.ValidateInstanceURL.
//   - Every outbound call runs through a dial Control that refuses link-local and
//     cloud-metadata addresses, and a CheckRedirect that refuses cross-host
//     redirects, so neither DNS nor a 302 can move the token off the validated
//     host.
//   - Resolving a ${secrets.X} token to plaintext is gated on
//     rbac.EnvironmentView, exactly as in the Kubernetes/pgvector proxies: the
//     resolved value authenticates a request on the user's behalf.
//   - SOQL has no bind variables over REST. Values are escaped (backslash first)
//     INCLUDING the LIKE wildcards, so a search for "50%" is a literal 50%;
//     identifiers cannot be quoted at all, so they are whitelist-validated.
//   - The describe cache is keyed on the CREDENTIAL as well as the org and
//     object. Describe output is filtered by the connected user's field-level
//     security, so a credential-blind key would leak one user's visible fields
//     into another user's dropdown.
//
// As with every option proxy the response is ALWAYS HTTP 200: {"options": [...]}
// on success, {"error": "..."} on failure, so the editor renders the message
// inline and the input falls back to manual entry.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// Constants and package state
// ---------------------------------------------------------------------------

const (
	// salesforceAPIVersion mirrors executor/actions/crm/salesforce.APIVersion, so
	// a dropdown describes the org against the same version the flow will call.
	salesforceAPIVersion = "62.0"

	// salesforceLookupLimit bounds a record picker. A hundred rows is more than a
	// select box can usefully show; past that the operator narrows the search or
	// types the id (the editor always allows free text).
	salesforceLookupLimit = 100

	// salesforceListLimit bounds the flat lists (users, owners, reports).
	salesforceListLimit = 200

	// salesforceMaxLookupObjects caps a polymorphic picker. WhatId's four common
	// targets are the widest real case; the cap stops a crafted endpoint turning
	// one dropdown into dozens of org queries.
	salesforceMaxLookupObjects = 5

	// salesforceMaxBody caps a response read. An sObject describe for a
	// well-used Case object runs to hundreds of KB.
	salesforceMaxBody = 8 << 20

	// salesforceRequestTimeout bounds one call. Describe is the slow one; the
	// editor is waiting on this to paint a dropdown, so it cannot be generous.
	salesforceRequestTimeout = 15 * time.Second
)

// salesforceOptionsHostOverride, when non-empty, replaces the org's instance URL
// as the base of every request AND relaxes host validation, so tests can point
// the proxies at an httptest server. Test-only; the same seam idiom as the
// executor's salesforce.testBaseURL.
var salesforceOptionsHostOverride = ""

// salesforceHostSuffixes are the domains a Salesforce org can legitimately live
// on. Mirrors the executor's list — the api must not depend on the executor
// module, so the guard is duplicated rather than imported.
var salesforceHostSuffixes = []string{
	".salesforce.com", // mycompany.my.salesforce.com, na1.salesforce.com
	".force.com",      // mycompany.lightning.force.com
	".salesforce.mil", // Government Cloud Plus
	".cloudforce.com", // legacy pods still in service
}

// salesforceObjectPattern matches an sObject API name, including a namespace
// prefix (Namespace__MyObject__c) and every custom suffix. Identifiers cannot be
// quoted in SOQL, so this whitelist — not escaping — is the only defence for
// them.
var salesforceObjectPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:__[A-Za-z][A-Za-z0-9_]*)*$`)

// salesforceFieldPattern matches a field identifier including relationship
// traversal (Account.Name, Custom__r.Owner.Email).
var salesforceFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)*$`)

// salesforceIDPattern matches a record id. Salesforce ids are 15 (case-sensitive)
// or 18 (case-safe) alphanumerics and nothing else.
var salesforceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{15,18}$`)

// salesforceSystemObjectSuffixes are the shadow objects the global describe is
// full of — AccountHistory, CaseShare, Contact__ChangeEvent — which are noise in
// an operator's object picker. Custom objects are exempted from the un-prefixed
// suffixes so a genuine "TimeShare__c" is never hidden.
var salesforceSystemObjectSuffixes = []string{
	"ChangeEvent", "__Share", "__History", "__Feed", "__Tag",
	"Share", "History", "Feed", "Tag",
}

// ---------------------------------------------------------------------------
// SSRF-hardened client
// ---------------------------------------------------------------------------

// salesforceOptionsDialControl refuses every internal destination. It runs on the
// address actually dialled, so a DNS name — or a redirect — resolving to one of
// them is caught too.
//
// This is STRICTER than the Jenkins / Kubernetes / pgvector guards, and
// deliberately so: those back self-hosted products whose whole point is that they
// live on the customer's LAN. Salesforce is SaaS. No org is ever reachable on
// loopback or RFC1918, so a *.salesforce.com name that resolves there is either
// DNS rebinding or a misconfiguration, and neither is worth following from a
// process that holds every tenant's secrets. Loopback is allowed back in only
// under the httptest seam.
func salesforceOptionsDialControl(_, address string, _ syscall.RawConn) error {
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
	if salesforceOptionsHostOverride != "" {
		return nil // test seam: the httptest server is on loopback
	}
	if ip.IsLoopback() {
		return errors.New("loopback addresses are not allowed")
	}
	if ip.IsPrivate() || ip.IsUnspecified() {
		return errors.New("private addresses are not allowed")
	}
	return nil
}

// salesforceOptionsHTTPClient is shared across the proxies so connections to
// orgs' API hosts are pooled. The dial Control blocks metadata IPs even
// mid-redirect; cross-host redirects are refused outright so a 302 can never
// carry the access token somewhere else.
var salesforceOptionsHTTPClient = &gohttp.Client{
	Timeout: salesforceRequestTimeout,
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
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: salesforceOptionsDialControl,
		}).DialContext,
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	},
}

// ---------------------------------------------------------------------------
// Instance URL handling
// ---------------------------------------------------------------------------

// normaliseSalesforceInstanceURL reduces whatever the operator pasted to a bare
// https origin, dropping userinfo, port, path and query so a crafted base cannot
// displace the API path appended to it. Mirrors the executor's
// salesforce.NormaliseInstanceURL.
func normaliseSalesforceInstanceURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// A bare host gets a scheme so it parses. An explicit http:// is upgraded:
	// Salesforce is https-only and silently downgrading would send the token in
	// clear.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return "https://" + host
}

// validateSalesforceInstanceURL confirms a normalised instance URL points at a
// Salesforce-owned host. This runs BEFORE the access token is attached — without
// it a crafted instance_url would exfiltrate the org's token to any host the
// caller names.
func validateSalesforceInstanceURL(normalised string) string {
	if normalised == "" {
		return "Set the Salesforce Instance URL to load this list (e.g. https://mycompany.my.salesforce.com)"
	}
	u, err := url.Parse(normalised)
	if err != nil || u.Host == "" {
		return "The Salesforce Instance URL is not a valid address — copy it from your browser while signed in to Salesforce"
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range salesforceHostSuffixes {
		// A real subdomain is required, not a bare suffix match, so
		// "salesforce.com" alone and "evilsalesforce.com" are both rejected.
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return ""
		}
	}
	return "The Salesforce Instance URL must be a Salesforce address ending in .salesforce.com or .force.com — copy it from your browser while signed in to Salesforce"
}

// ---------------------------------------------------------------------------
// Connection resolution
// ---------------------------------------------------------------------------

// salesforceProxyConn is one validated connection to an org.
type salesforceProxyConn struct {
	BaseURL string // https://host, or the test override
	Token   string
	// Fingerprint identifies the credential+org pair for the describe cache. It
	// is a hash, never the token itself, so the cache map cannot become a place
	// tokens are read out of.
	Fingerprint string
}

// resolveSalesforceConn reads the node's Salesforce connection out of the query
// parameters. access_token is a Secret slot, so it can hold either a managed
// ${credentials.X} from the "Connect Salesforce" flow or a pasted ${secrets.X};
// both are resolved server-side and the plaintext never transits the browser.
//
// A non-empty message is operator-facing text to render in place of the dropdown
// and the caller must stop. An empty message with ok == false means
// checkPermission already wrote the response.
func (s *Service) resolveSalesforceConn(c *gin.Context) (salesforceProxyConn, string, bool) {
	tokenRaw := strings.TrimSpace(c.Query("access_token"))
	instanceRaw := strings.TrimSpace(c.Query("instance_url"))
	environmentID := strings.TrimSpace(c.Query("environment"))

	if tokenRaw == "" {
		return salesforceProxyConn{}, "Connect Salesforce (or select a secret holding an access token) to load this list", false
	}

	var token, managedInstance string

	if name := managedCredentialName(tokenRaw); name != "" {
		// Managed "Connect Salesforce" credential. The Salesforce
		// credential_provider row ships in v1, so a Connect-authed node MUST get
		// working dropdowns — otherwise the managed path is worse UX than a
		// pasted token, which is the wrong way round.
		if environmentID == "" {
			return salesforceProxyConn{}, "Select an environment to load this list", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return salesforceProxyConn{}, "", false // checkPermission wrote the response
		}
		resolved, instance, errMsg := s.resolveSalesforceCredential(c, environmentID, name)
		if errMsg != "" {
			return salesforceProxyConn{}, errMsg, false
		}
		token, managedInstance = resolved, instance
	} else if strings.HasPrefix(tokenRaw, "${") {
		if environmentID == "" {
			return salesforceProxyConn{}, "Select an environment to resolve the Salesforce access token", false
		}
		// Resolving a secret to plaintext must be gated by the same permission as
		// reading it through the environment endpoints: the resolved value
		// authenticates a request to a caller-influenced host, so without this
		// check a member denied environment.view could exfiltrate any secret.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return salesforceProxyConn{}, "", false // checkPermission wrote the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, tokenRaw)
		if errMsg != "" {
			return salesforceProxyConn{}, errMsg, false
		}
		token = resolved
	} else {
		token = tokenRaw
	}

	if token == "" {
		return salesforceProxyConn{}, "Connect Salesforce (or select a secret holding an access token) to load this list", false
	}

	// The Instance URL input is normally bound to
	// ${credentials.<name>.instance_url} on a Connect-authed node, which the
	// editor cannot resolve — so the credential's own captured value is used.
	instance := instanceRaw
	if strings.HasPrefix(instance, "${") || instance == "" {
		if managedInstance != "" {
			instance = managedInstance
		} else if strings.HasPrefix(instance, "${secrets.") || strings.HasPrefix(instance, "${secret.") {
			if environmentID == "" {
				return salesforceProxyConn{}, "Select an environment to resolve the Salesforce Instance URL", false
			}
			if !s.checkPermission(c, rbac.EnvironmentView) {
				return salesforceProxyConn{}, "", false // checkPermission wrote the response
			}
			resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, instance)
			if errMsg != "" {
				return salesforceProxyConn{}, errMsg, false
			}
			instance = resolved
		} else if instance != "" {
			// A flow variable the editor could not resolve here. It is not a host.
			return salesforceProxyConn{}, "The Salesforce Instance URL is set from a variable, so this list can't be loaded — type the value in", false
		}
	}

	base := normaliseSalesforceInstanceURL(instance)
	if salesforceOptionsHostOverride == "" {
		if errMsg := validateSalesforceInstanceURL(base); errMsg != "" {
			return salesforceProxyConn{}, errMsg, false
		}
	} else {
		base = salesforceOptionsHostOverride
	}

	// Hash rather than store: the fingerprint is a map key held for minutes, and
	// nothing downstream needs the token back out of it.
	sum := sha256.Sum256([]byte(token + "\x00" + base))
	return salesforceProxyConn{BaseURL: base, Token: token, Fingerprint: hex.EncodeToString(sum[:])}, "", true
}

// resolveSalesforceCredential resolves a managed ${credentials.X} Salesforce
// connection to its access token and the org API host captured on the OAuth token
// response (see captureProviderTenant). The caller has already gated on
// EnvironmentView; the environment is looked up user-scoped so a requester can
// only resolve credentials in an environment they can already view.
func (s *Service) resolveSalesforceCredential(c *gin.Context, environmentID, name string) (string, string, string) {
	user := s.getUserFromContext(c)
	if user == nil {
		return "", "", "unauthorized"
	}
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		return "", "", "Environment not found"
	}

	token, metaRaw, err := s.persistence.GetCredentialWithMetaByName(environmentID, name, env.SecretKey)
	if err != nil || token == nil || *token == "" {
		return "", "", fmt.Sprintf("The Salesforce connection %q isn't set up in this environment — reconnect Salesforce", name)
	}

	instance := ""
	if metaRaw != nil {
		var meta struct {
			InstanceURL string `json:"instance_url"`
		}
		if err := json.Unmarshal(*metaRaw, &meta); err == nil {
			instance = strings.TrimSpace(meta.InstanceURL)
		}
	}
	return *token, instance, ""
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// salesforceStatusError carries the HTTP status and the org's own error code so
// salesforceErrorMessage can turn both into something an operator can act on.
type salesforceStatusError struct {
	status  int
	code    string
	message string
}

func (e *salesforceStatusError) Error() string {
	return fmt.Sprintf("salesforce responded %d (%s): %s", e.status, e.code, e.message)
}

// salesforceGet performs one authenticated GET below the version root and
// returns the raw body. path must start with "/" (e.g. "/sobjects/Lead/describe").
func salesforceGet(c *gin.Context, conn salesforceProxyConn, path string) ([]byte, error) {
	endpoint := conn.BaseURL + "/services/data/v" + salesforceAPIVersion + path

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := salesforceOptionsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, salesforceMaxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, message := salesforceDecodeError(body)
		return nil, &salesforceStatusError{status: resp.StatusCode, code: code, message: message}
	}
	return body, nil
}

// salesforceQuery runs one SOQL statement. Every caller assembles its own SOQL
// because Salesforce has no bind-variable syntax over REST — which is exactly why
// nothing reaches this function that has not been escaped or whitelisted.
func salesforceQuery(c *gin.Context, conn salesforceProxyConn, soql string) ([]map[string]any, error) {
	body, err := salesforceGet(c, conn, "/query?q="+url.QueryEscape(soql))
	if err != nil {
		return nil, err
	}
	var out struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Records, nil
}

// salesforceDecodeError pulls the errorCode and message out of Salesforce's
// error body. Salesforce returns a JSON ARRAY — [{"message":…,"errorCode":…}] —
// a shape no other Flomation integration uses.
func salesforceDecodeError(body []byte) (string, string) {
	var arr []struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return arr[0].ErrorCode, arr[0].Message
	}
	var obj struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &obj); err == nil && obj.Error != "" {
		return obj.Error, obj.Description
	}
	return "", ""
}

// salesforceErrorMessage maps a failure to operator-facing text. The raw
// errorCode is never shown: INVALID_CROSS_REFERENCE_KEY means nothing to the
// person reading it, and the whole point of the picker is that they should not
// have to know Salesforce's vocabulary.
func salesforceErrorMessage(err error, what string) string {
	var statusErr *salesforceStatusError
	if errors.As(err, &statusErr) {
		// The org's own errorCode is matched FIRST, and the HTTP status only as a
		// fallback. Salesforce overloads its statuses: REQUEST_LIMIT_EXCEEDED comes
		// back as 403, so a status-first switch reports an exhausted API allowance
		// as a permissions problem and sends the operator to their administrator
		// when the actual fix is to wait a few minutes.
		switch statusErr.code {
		case "INVALID_SESSION_ID":
			return "Your Salesforce session has expired — reconnect Salesforce (or refresh the access token) to load this list"
		case "INSUFFICIENT_ACCESS", "INSUFFICIENT_ACCESS_OR_READONLY":
			return fmt.Sprintf("Your Salesforce user isn't allowed to see the %s — ask your Salesforce administrator, or type the value in", what)
		case "REQUEST_LIMIT_EXCEEDED":
			return "Your Salesforce org has hit its API request limit — try again shortly, or type the value in"
		case "QUERY_TIMEOUT":
			return fmt.Sprintf("Salesforce took too long to return the %s — type the value in (the flow itself still runs)", what)
		case "NOT_FOUND", "INVALID_TYPE", "INVALID_TYPE_FOR_OPERATION":
			return "Salesforce doesn't recognise that object in your org — check the Object, or type the value in"
		case "INVALID_FIELD":
			return fmt.Sprintf("Salesforce doesn't have that field on this object, so the %s can't be listed — type the value in", what)
		}
		switch statusErr.status {
		case gohttp.StatusUnauthorized:
			return "Your Salesforce session has expired — reconnect Salesforce (or refresh the access token) to load this list"
		case gohttp.StatusForbidden:
			return fmt.Sprintf("Your Salesforce user isn't allowed to see the %s — ask your Salesforce administrator, or type the value in", what)
		case gohttp.StatusNotFound:
			return "Salesforce doesn't recognise that object in your org — check the Object, or type the value in"
		case gohttp.StatusTooManyRequests:
			return "Salesforce is rate-limiting this org — try again shortly, or type the value in"
		}
		return fmt.Sprintf("Salesforce couldn't return the %s (it answered HTTP %d) — type the value in", what, statusErr.status)
	}
	return fmt.Sprintf("Could not reach Salesforce to load the %s — check the Instance URL and that the connection is still authorised", what)
}

// ---------------------------------------------------------------------------
// SOQL construction — the injection boundary
// ---------------------------------------------------------------------------

// salesforceLikeEscaper escapes a value for a quoted SOQL LIKE literal.
//
// The backslash rule comes first, and strings.Replacer makes ONE left-to-right
// pass over the input, so the backslashes it introduces are never re-processed —
// the double-escaping trap the executor's EscapeSOQLString documents.
//
// The two entries no plain-literal escaper has are '%' and '_': inside LIKE they
// are wildcards, so an unescaped search for "50%" would match everything
// beginning "50". Salesforce accepts backslash-escaped wildcards in LIKE.
var salesforceLikeEscaper = strings.NewReplacer(
	`\`, `\\`,
	`'`, `\'`,
	`"`, `\"`,
	`%`, `\%`,
	`_`, `\_`,
	"\n", `\n`,
	"\r", `\r`,
	"\t", `\t`,
)

// salesforceLiteralEscaper escapes a value for an ordinary quoted SOQL literal
// (an id in a WHERE clause), where % and _ carry no special meaning.
var salesforceLiteralEscaper = strings.NewReplacer(
	`\`, `\\`,
	`'`, `\'`,
	`"`, `\"`,
	"\n", `\n`,
	"\r", `\r`,
	"\t", `\t`,
)

// validateSalesforceObject whitelist-validates an sObject API name and returns
// operator-facing text when it is not one. Identifiers cannot be quoted in SOQL,
// so this is the only defence available for them.
func validateSalesforceObject(object string) (string, string) {
	object = strings.TrimSpace(object)
	if object == "" {
		return "", "Choose the Salesforce Object first to load this list"
	}
	if strings.HasPrefix(object, "${") {
		return "", "The Salesforce Object is set from a variable, so this list can't be loaded — type the value in"
	}
	if !salesforceObjectPattern.MatchString(object) {
		return "", fmt.Sprintf("%q is not a Salesforce object name — use the API name, e.g. Account or Invoice__c", object)
	}
	return object, ""
}

// validateSalesforceField whitelist-validates a field identifier, allowing the
// dots of relationship traversal (Contact.Name).
func validateSalesforceField(field string) (string, string) {
	field = strings.TrimSpace(field)
	if field == "" {
		return "", "Choose the field first to load this list"
	}
	if strings.HasPrefix(field, "${") {
		return "", "That field is set from a variable, so this list can't be loaded — type the value in"
	}
	if !salesforceFieldPattern.MatchString(field) {
		return "", fmt.Sprintf("%q is not a Salesforce field name — use the API name, e.g. StageName or Customer_Tier__c", field)
	}
	return field, ""
}

// salesforceQueryObject reads the object an endpoint is scoped to. It is taken
// from whichever input the calling action actually has: `object` on the generic
// record actions, `custom_object` on the custom-object ones, `link_to_object` on
// file_upload. The picker markers forward exactly one of them.
func salesforceQueryObject(c *gin.Context) string {
	for _, p := range []string{"object", "custom_object", "link_to_object"} {
		if v := strings.TrimSpace(c.Query(p)); v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Describe, and its per-credential cache
// ---------------------------------------------------------------------------

// salesforceDescribeField is the slice of an sObject describe field the pickers
// need. The full describe is discarded after decoding: a Case describe runs to
// hundreds of KB, almost all of it layout and relationship detail no dropdown
// reads.
type salesforceDescribeField struct {
	Name           string
	Label          string
	Type           string
	Createable     bool
	Updateable     bool
	Filterable     bool
	Sortable       bool
	ExternalID     bool
	IDLookup       bool
	NameField      bool
	PicklistValues []api.InputOption
}

// salesforceDescribeRecordType is one active record type.
type salesforceDescribeRecordType struct {
	ID     string
	Name   string
	Master bool
}

// salesforceDescribe is the decoded, trimmed describe of one object.
type salesforceDescribe struct {
	Fields      []salesforceDescribeField
	RecordTypes []salesforceDescribeRecordType
}

// salesforceGlobalObject is one entry of the global describe.
type salesforceGlobalObject struct {
	Name   string
	Label  string
	Custom bool
}

// The describe cache. Two properties are load-bearing:
//
//   - The key includes the credential fingerprint. Describe output is filtered by
//     the connected user's field-level security, so a key of just org+object
//     would serve one user's visible fields to another user of the same org.
//   - It is bounded and expiring. A dropdown cache that grows with the number of
//     objects any tenant has ever opened is a slow memory leak in a process that
//     also holds every tenant's secrets.
const (
	salesforceCacheTTL     = 10 * time.Minute
	salesforceCacheMaxSize = 128
)

type salesforceCacheEntry struct {
	value   any
	expires time.Time
}

var (
	salesforceCacheMu sync.Mutex
	salesforceCache   = map[string]salesforceCacheEntry{}
)

func salesforceCacheGet(key string) (any, bool) {
	salesforceCacheMu.Lock()
	defer salesforceCacheMu.Unlock()
	entry, ok := salesforceCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(salesforceCache, key)
		return nil, false
	}
	return entry.value, true
}

func salesforceCachePut(key string, value any) {
	salesforceCacheMu.Lock()
	defer salesforceCacheMu.Unlock()
	if len(salesforceCache) >= salesforceCacheMaxSize {
		// Drop everything already expired; if that frees nothing, drop the entry
		// closest to expiry. Both passes are over a map capped at 128 entries.
		now := time.Now()
		oldestKey, oldest := "", time.Time{}
		for k, v := range salesforceCache {
			if now.After(v.expires) {
				delete(salesforceCache, k)
				continue
			}
			if oldest.IsZero() || v.expires.Before(oldest) {
				oldestKey, oldest = k, v.expires
			}
		}
		if len(salesforceCache) >= salesforceCacheMaxSize && oldestKey != "" {
			delete(salesforceCache, oldestKey)
		}
	}
	salesforceCache[key] = salesforceCacheEntry{value: value, expires: time.Now().Add(salesforceCacheTTL)}
}

// salesforceDescribeObject fetches (or serves from cache) one object's describe.
func salesforceDescribeObject(c *gin.Context, conn salesforceProxyConn, object string) (*salesforceDescribe, error) {
	key := "sobject\x00" + conn.Fingerprint + "\x00" + object
	if cached, ok := salesforceCacheGet(key); ok {
		if d, ok := cached.(*salesforceDescribe); ok {
			return d, nil
		}
	}

	body, err := salesforceGet(c, conn, "/sobjects/"+url.PathEscape(object)+"/describe")
	if err != nil {
		return nil, err
	}

	var raw struct {
		Fields []struct {
			Name           string `json:"name"`
			Label          string `json:"label"`
			Type           string `json:"type"`
			Createable     bool   `json:"createable"`
			Updateable     bool   `json:"updateable"`
			Filterable     bool   `json:"filterable"`
			Sortable       bool   `json:"sortable"`
			ExternalID     bool   `json:"externalId"`
			IDLookup       bool   `json:"idLookup"`
			NameField      bool   `json:"nameField"`
			PicklistValues []struct {
				Active bool   `json:"active"`
				Label  string `json:"label"`
				Value  string `json:"value"`
			} `json:"picklistValues"`
		} `json:"fields"`
		RecordTypeInfos []struct {
			RecordTypeID string `json:"recordTypeId"`
			Name         string `json:"name"`
			Active       bool   `json:"active"`
			Available    bool   `json:"available"`
			Master       bool   `json:"master"`
		} `json:"recordTypeInfos"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	out := &salesforceDescribe{Fields: make([]salesforceDescribeField, 0, len(raw.Fields))}
	for _, f := range raw.Fields {
		field := salesforceDescribeField{
			Name: f.Name, Label: f.Label, Type: f.Type,
			Createable: f.Createable, Updateable: f.Updateable,
			Filterable: f.Filterable, Sortable: f.Sortable,
			ExternalID: f.ExternalID, IDLookup: f.IDLookup, NameField: f.NameField,
		}
		for _, pv := range f.PicklistValues {
			// The n8n defect this exists to avoid: a retired picklist value is
			// still in the describe, and offering it produces a write Salesforce
			// rejects.
			if !pv.Active {
				continue
			}
			label := pv.Label
			if label == "" {
				label = pv.Value
			}
			field.PicklistValues = append(field.PicklistValues, api.InputOption{Name: label, Value: pv.Value})
		}
		out.Fields = append(out.Fields, field)
	}
	for _, rt := range raw.RecordTypeInfos {
		if !rt.Active || !rt.Available {
			continue
		}
		out.RecordTypes = append(out.RecordTypes, salesforceDescribeRecordType{
			ID: rt.RecordTypeID, Name: rt.Name, Master: rt.Master,
		})
	}

	salesforceCachePut(key, out)
	return out, nil
}

// salesforceDescribeGlobal fetches (or serves from cache) the org's object list.
func salesforceDescribeGlobal(c *gin.Context, conn salesforceProxyConn) ([]salesforceGlobalObject, error) {
	key := "global\x00" + conn.Fingerprint
	if cached, ok := salesforceCacheGet(key); ok {
		if objs, ok := cached.([]salesforceGlobalObject); ok {
			return objs, nil
		}
	}

	body, err := salesforceGet(c, conn, "/sobjects/")
	if err != nil {
		return nil, err
	}
	var raw struct {
		SObjects []struct {
			Name                string `json:"name"`
			Label               string `json:"label"`
			Custom              bool   `json:"custom"`
			Queryable           bool   `json:"queryable"`
			DeprecatedAndHidden bool   `json:"deprecatedAndHidden"`
		} `json:"sobjects"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	objects := make([]salesforceGlobalObject, 0, len(raw.SObjects))
	for _, o := range raw.SObjects {
		if !o.Queryable || o.DeprecatedAndHidden {
			continue
		}
		if salesforceIsSystemObject(o.Name, o.Custom) {
			continue
		}
		objects = append(objects, salesforceGlobalObject{Name: o.Name, Label: o.Label, Custom: o.Custom})
	}

	salesforceCachePut(key, objects)
	return objects, nil
}

// salesforceIsSystemObject hides the shadow objects every org carries — the
// per-object Share / History / Feed / Tag / ChangeEvent tables. They are a third
// of the global describe and none of them is ever what an operator meant. The
// bare (un-prefixed) suffixes are only applied to standard objects so a genuine
// custom "TimeShare__c" survives.
func salesforceIsSystemObject(name string, custom bool) bool {
	for _, suffix := range salesforceSystemObjectSuffixes {
		if !strings.HasPrefix(suffix, "__") && custom {
			continue
		}
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			return true
		}
	}
	return false
}

// salesforceSortOptions orders a list the way a person reads it. Picklists are
// deliberately NOT sorted through here — Salesforce's own order is meaningful
// (opportunity stages run Prospecting → Closed Won, not alphabetically).
func salesforceSortOptions(options []api.InputOption) {
	sort.SliceStable(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
}

// salesforceRespond is the single exit point: always HTTP 200, options on
// success and a sentence on failure, so the editor renders the message inline
// and the input falls back to manual entry.
func salesforceRespond(c *gin.Context, options []api.InputOption, errMsg string) {
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	if options == nil {
		options = []api.InputOption{}
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// salesforceConn resolves the connection and writes the failure response itself,
// so every handler starts with the same three lines.
func (s *Service) salesforceConn(c *gin.Context) (salesforceProxyConn, bool) {
	conn, errMsg, ok := s.resolveSalesforceConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return salesforceProxyConn{}, false
	}
	return conn, true
}

// ---------------------------------------------------------------------------
// 1. Objects
// ---------------------------------------------------------------------------

// getSalesforceObjects serves the org's objects for every Object / Custom Object
// input. ?custom_only=true narrows it to the org's own objects, which is what the
// custom-object actions want.
func (s *Service) getSalesforceObjects(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	customOnly := strings.EqualFold(strings.TrimSpace(c.Query("custom_only")), "true")

	objects, err := salesforceDescribeGlobal(c, conn)
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce objects")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of objects"))
		return
	}

	options := make([]api.InputOption, 0, len(objects))
	for _, o := range objects {
		if customOnly && !o.Custom {
			continue
		}
		label := o.Label
		if label == "" {
			label = o.Name
		}
		options = append(options, api.InputOption{Name: label, Value: o.Name})
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 2. Fields
// ---------------------------------------------------------------------------

// salesforceFieldFilters is the closed set of ?filter= values. The filter is OUR
// parameter — it is baked into the endpoint URL in the marker, never typed by an
// operator — so anything outside this set is a bug or a hand-crafted request.
var salesforceFieldFilters = map[string]struct{}{
	"all": {}, "createable": {}, "updateable": {}, "picklist": {},
}

// getSalesforceFields serves one object's fields for the Fields / Filter Field /
// Sort By / Field to Set / Look Up By / Dropdown Field inputs.
func (s *Service) getSalesforceFields(c *gin.Context) {
	filter := strings.TrimSpace(c.Query("filter"))
	if filter == "" {
		filter = "all"
	}
	if _, known := salesforceFieldFilters[filter]; !known {
		salesforceRespond(c, nil, "Unknown field filter")
		return
	}

	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	describe, err := salesforceDescribeObject(c, conn, object)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": object}).Warn("unable to describe Salesforce object for fields")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of fields"))
		return
	}

	options := make([]api.InputOption, 0, len(describe.Fields))
	for _, f := range describe.Fields {
		switch filter {
		case "createable":
			if !f.Createable {
				continue
			}
		case "updateable":
			if !f.Updateable {
				continue
			}
		case "picklist":
			if f.Type != "picklist" && f.Type != "multipicklist" && f.Type != "combobox" {
				continue
			}
		}
		label := f.Label
		if label == "" {
			label = f.Name
		}
		options = append(options, api.InputOption{Name: label, Value: f.Name})
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 3. Picklist values
// ---------------------------------------------------------------------------

// getSalesforcePicklistValues serves one picklist field's ACTIVE values. This
// single endpoint backs every picklist input in the node — Lead Status, Stage,
// Case Origin, Task Priority, the User locale keys — because they are all the
// same question asked of a different (object, field) pair, and the pair rides in
// the endpoint URL of each marker.
func (s *Service) getSalesforcePicklistValues(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}
	field, errMsg := validateSalesforceField(c.Query("field"))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	describe, err := salesforceDescribeObject(c, conn, object)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": object, "field": field}).
			Warn("unable to describe Salesforce object for picklist values")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of choices"))
		return
	}

	for _, f := range describe.Fields {
		if !strings.EqualFold(f.Name, field) {
			continue
		}
		if len(f.PicklistValues) == 0 {
			label := f.Label
			if label == "" {
				label = f.Name
			}
			salesforceRespond(c, nil, fmt.Sprintf(
				"%s isn't a dropdown field in your org, so there are no set choices — type the value in", label))
			return
		}
		// NOT sorted: Salesforce's picklist order is the order Setup defines, and
		// for stages and statuses that order is the process itself.
		salesforceRespond(c, f.PicklistValues, "")
		return
	}
	salesforceRespond(c, nil, fmt.Sprintf("Your org's %s object has no %s field — type the value in", object, field))
}

// ---------------------------------------------------------------------------
// 4. External-id fields
// ---------------------------------------------------------------------------

// getSalesforceExternalIDFields serves the fields an upsert can match on: the
// org's External Id fields, plus the id-lookup fields Salesforce accepts as keys
// (Id itself, and unique fields such as Contact.Email where enabled).
func (s *Service) getSalesforceExternalIDFields(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	describe, err := salesforceDescribeObject(c, conn, object)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": object}).
			Warn("unable to describe Salesforce object for external id fields")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of matching fields"))
		return
	}

	options := make([]api.InputOption, 0, 8)
	for _, f := range describe.Fields {
		if !f.ExternalID && !f.IDLookup {
			continue
		}
		label := f.Label
		if label == "" {
			label = f.Name
		}
		options = append(options, api.InputOption{Name: label, Value: f.Name})
	}
	if len(options) == 0 {
		salesforceRespond(c, nil, fmt.Sprintf("Your org's %s object has no External Id field set up — add one in Salesforce Setup, or match on the record Id", object))
		return
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 5. Record types
// ---------------------------------------------------------------------------

// getSalesforceRecordTypes serves one object's active record types. The Master
// record type is excluded: it is present in every describe but naming it is never
// what an operator means, and orgs that use record types always have real ones.
func (s *Service) getSalesforceRecordTypes(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	describe, err := salesforceDescribeObject(c, conn, object)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": object}).
			Warn("unable to describe Salesforce object for record types")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of record types"))
		return
	}

	options := make([]api.InputOption, 0, len(describe.RecordTypes))
	for _, rt := range describe.RecordTypes {
		if rt.Master {
			continue
		}
		options = append(options, api.InputOption{Name: rt.Name, Value: rt.ID})
	}
	if len(options) == 0 {
		salesforceRespond(c, nil, fmt.Sprintf("Your org doesn't use record types on %s — leave this blank", object))
		return
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 6. Record lookup
// ---------------------------------------------------------------------------

// getSalesforceLookup is the searchable record picker behind every *_id input
// that names an existing record — Account, Contact, Lead, Opportunity, Campaign,
// Case, Task, Event, and the polymorphic WhoId / WhatId pairs.
//
// Three things make it different from n8n's equivalent:
//
//   - The query is filtered and LIMITed SERVER-SIDE. n8n pages the entire object
//     on every dropdown open.
//   - The label column is discovered from describe rather than assumed to be
//     Name. Case has no Name (it is CaseNumber), Task and Event use Subject,
//     ContentDocument uses Title — a hard-coded "SELECT Id, Name" fails outright
//     on all four.
//   - `object` may name SEVERAL objects (WhoId is a Contact OR a Lead). Each is
//     queried under its own bound LIMIT and the results are merged with an
//     unambiguous "Contact: …" / "Lead: …" prefix.
func (s *Service) getSalesforceLookup(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	raw := salesforceQueryObject(c)
	if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "${") {
		salesforceRespond(c, nil, "Choose the Salesforce Object first to search for a record")
		return
	}
	var objects []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		object, errMsg := validateSalesforceObject(part)
		if errMsg != "" {
			salesforceRespond(c, nil, errMsg)
			return
		}
		objects = append(objects, object)
	}
	if len(objects) == 0 {
		salesforceRespond(c, nil, "Choose the Salesforce Object first to search for a record")
		return
	}
	if len(objects) > salesforceMaxLookupObjects {
		objects = objects[:salesforceMaxLookupObjects]
	}

	search := strings.TrimSpace(c.Query("search"))
	if strings.HasPrefix(search, "${") {
		search = ""
	}

	options := make([]api.InputOption, 0, salesforceLookupLimit)
	var lastErr error
	for _, object := range objects {
		rows, err := salesforceLookupOne(c, conn, object, search, len(objects) > 1)
		if err != nil {
			// One object of a polymorphic set failing (a Lead the user cannot see,
			// say) must not blank the whole picker.
			lastErr = err
			log.WithFields(log.Fields{"error": err, "object": object}).Warn("unable to search Salesforce records")
			continue
		}
		options = append(options, rows...)
	}
	if len(options) == 0 && lastErr != nil {
		salesforceRespond(c, nil, salesforceErrorMessage(lastErr, "list of records"))
		return
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// salesforceLookupOne runs the bounded search for a single object.
func salesforceLookupOne(c *gin.Context, conn salesforceProxyConn, object, search string, prefix bool) ([]api.InputOption, error) {
	describe, err := salesforceDescribeObject(c, conn, object)
	if err != nil {
		return nil, err
	}

	// Find the object's own name field. Salesforce flags exactly one field with
	// nameField; the fallbacks cover the handful of objects that flag none.
	var nameField *salesforceDescribeField
	for i := range describe.Fields {
		if describe.Fields[i].NameField {
			nameField = &describe.Fields[i]
			break
		}
	}
	if nameField == nil {
		for _, candidate := range []string{"Name", "Subject", "Title", "CaseNumber", "DeveloperName"} {
			for i := range describe.Fields {
				if strings.EqualFold(describe.Fields[i].Name, candidate) {
					nameField = &describe.Fields[i]
					break
				}
			}
			if nameField != nil {
				break
			}
		}
	}
	if nameField == nil {
		return nil, &salesforceStatusError{
			status: gohttp.StatusBadRequest, code: "INVALID_FIELD",
			message: object + " has no name field",
		}
	}

	// A second column is selected when the name alone does not identify the
	// record — a Case's number means nothing without its subject. Only ever a
	// field the describe actually reports, so this cannot produce INVALID_FIELD.
	secondary := ""
	if candidate, wanted := salesforceLookupSecondaryField[object]; wanted {
		for _, f := range describe.Fields {
			if strings.EqualFold(f.Name, candidate) {
				secondary = f.Name
				break
			}
		}
	}

	selectList := "Id, " + nameField.Name
	if secondary != "" {
		selectList += ", " + secondary
	}
	soql := "SELECT " + selectList + " FROM " + object
	// The search term is the ONLY caller-supplied value in this statement, and it
	// goes inside a quoted LIKE literal with the wildcards escaped.
	if search != "" && nameField.Filterable {
		soql += " WHERE " + nameField.Name + " LIKE '%" + salesforceLikeEscaper.Replace(search) + "%'"
	}
	if nameField.Sortable {
		soql += " ORDER BY " + nameField.Name
	}
	soql += fmt.Sprintf(" LIMIT %d", salesforceLookupLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		return nil, err
	}

	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		id, _ := r["Id"].(string)
		if id == "" {
			continue
		}
		label := salesforceStringValue(r[nameField.Name])
		if secondary != "" {
			if extra := salesforceStringValue(r[secondary]); extra != "" {
				if label == "" {
					label = extra
				} else {
					label += " — " + extra
				}
			}
		}
		if label == "" {
			label = id
		}
		if prefix {
			label = object + ": " + label
		}
		options = append(options, api.InputOption{Name: label, Value: id})
	}
	return options, nil
}

// salesforceLookupSecondaryField names a second column worth showing beside the
// object's name field, for the objects whose name is an opaque reference.
var salesforceLookupSecondaryField = map[string]string{
	"Case": "Subject", // CaseNumber alone tells the operator nothing
}

// salesforceStringValue coerces a SOQL scalar to display text. Salesforce returns
// numbers for auto-number fields, so a plain type assertion drops them.
func salesforceStringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.4f", t), "0"), ".")
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// ---------------------------------------------------------------------------
// 7. Users
// ---------------------------------------------------------------------------

// getSalesforceUsers serves the org's ACTIVE users. Deactivated users are the
// n8n defect this exists to avoid: Salesforce rejects them on write with
// INVALID_CROSS_REFERENCE_KEY, which reads like a broken integration rather than
// a stale choice. ?include_inactive=true is available for the rare case a flow
// really does need one.
func (s *Service) getSalesforceUsers(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	options, err := salesforceFetchUsers(c, conn,
		strings.EqualFold(strings.TrimSpace(c.Query("include_inactive")), "true"), "")
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce users")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of users"))
		return
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// salesforceFetchUsers runs the bounded user query. prefix, when set, is put in
// front of every label (the owners picker labels its user group).
func salesforceFetchUsers(c *gin.Context, conn salesforceProxyConn, includeInactive bool, prefix string) ([]api.InputOption, error) {
	soql := "SELECT Id, Name, Username FROM User"
	if !includeInactive {
		soql += " WHERE IsActive = true"
	}
	soql += fmt.Sprintf(" ORDER BY Name LIMIT %d", salesforceListLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		return nil, err
	}
	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		id, _ := r["Id"].(string)
		if id == "" {
			continue
		}
		label := salesforceStringValue(r["Name"])
		if label == "" {
			label = id
		}
		// Two Jane Smiths in one org is ordinary; the username disambiguates them.
		if username := salesforceStringValue(r["Username"]); username != "" {
			label += " (" + username + ")"
		}
		options = append(options, api.InputOption{Name: prefix + label, Value: id})
	}
	return options, nil
}

// ---------------------------------------------------------------------------
// 8. Owners (users AND queues)
// ---------------------------------------------------------------------------

// getSalesforceOwners serves everything an OwnerId can legitimately be set to for
// one object: the org's active users AND the queues assigned to that object.
//
// Both groups are prefixed unconditionally. n8n prefixes users only when a queue
// happens to exist, so the identical field reads "Jane Smith" in one org and
// "User: Jane Smith" in another — and an operator comparing two orgs cannot tell
// whether the difference is meaningful. Lead.OwnerId and Case.OwnerId genuinely
// take a queue id, so leaving queues out is not an option either.
func (s *Service) getSalesforceOwners(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	users, err := salesforceFetchUsers(c, conn, false, "User: ")
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce users for owners")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of owners"))
		return
	}
	salesforceSortOptions(users)

	// Queues come second and are allowed to fail on their own: QueueSobject needs
	// a permission a read-only integration user may not have, and losing the
	// queues is far better than losing the whole picker.
	queueSOQL := "SELECT QueueId, Queue.Name FROM QueueSobject WHERE SobjectType = '" +
		salesforceLiteralEscaper.Replace(object) + "'" +
		fmt.Sprintf(" ORDER BY Queue.Name LIMIT %d", salesforceListLimit)

	queues := make([]api.InputOption, 0, 8)
	if records, qErr := salesforceQuery(c, conn, queueSOQL); qErr != nil {
		log.WithFields(log.Fields{"error": qErr, "object": object}).
			Info("Salesforce queues unavailable for owner picker — listing users only")
	} else {
		for _, r := range records {
			id, _ := r["QueueId"].(string)
			if id == "" {
				continue
			}
			name := ""
			if q, isMap := r["Queue"].(map[string]any); isMap {
				name = salesforceStringValue(q["Name"])
			}
			if name == "" {
				name = id
			}
			queues = append(queues, api.InputOption{Name: "Queue: " + name, Value: id})
		}
		salesforceSortOptions(queues)
	}

	salesforceRespond(c, append(queues, users...), "")
}

// ---------------------------------------------------------------------------
// 9. Campaign member statuses (two-hop)
// ---------------------------------------------------------------------------

// getSalesforceCampaignMemberStatus serves one campaign's member statuses. It is
// the node's only two-hop picker: the statuses are defined per campaign, so the
// list depends on the campaign the operator has already chosen (which is itself
// filled by /salesforce-lookup).
func (s *Service) getSalesforceCampaignMemberStatus(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	campaignID := strings.TrimSpace(c.Query("campaign_id"))
	if campaignID == "" || strings.HasPrefix(campaignID, "${") {
		salesforceRespond(c, nil, "Choose the Campaign first to load its member statuses")
		return
	}
	if !salesforceIDPattern.MatchString(campaignID) {
		salesforceRespond(c, nil, "That Campaign ID doesn't look like a Salesforce record ID — choose the campaign from the list")
		return
	}

	soql := "SELECT Label, SortOrder, IsDefault FROM CampaignMemberStatus WHERE CampaignId = '" +
		salesforceLiteralEscaper.Replace(campaignID) + "' ORDER BY SortOrder LIMIT " +
		fmt.Sprint(salesforceListLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "campaign": campaignID}).
			Warn("unable to list Salesforce campaign member statuses")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of member statuses"))
		return
	}

	// CampaignMember.Status holds the status LABEL, not an id, so the label is
	// both what the operator sees and what the flow sends.
	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		label := salesforceStringValue(r["Label"])
		if label == "" {
			continue
		}
		name := label
		if isDefault, _ := r["IsDefault"].(bool); isDefault {
			name += " (default)"
		}
		options = append(options, api.InputOption{Name: name, Value: label})
	}
	// NOT sorted: SortOrder is the sequence the campaign's own setup defines.
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 10. List views
// ---------------------------------------------------------------------------

// getSalesforceListViews serves one object's list views for the List View input.
func (s *Service) getSalesforceListViews(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	object, errMsg := validateSalesforceObject(salesforceQueryObject(c))
	if errMsg != "" {
		salesforceRespond(c, nil, errMsg)
		return
	}

	body, err := salesforceGet(c, conn, fmt.Sprintf("/sobjects/%s/listviews?limit=%d",
		url.PathEscape(object), salesforceListLimit))
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": object}).Warn("unable to list Salesforce list views")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list views"))
		return
	}

	var raw struct {
		ListViews []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"listviews"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		salesforceRespond(c, nil, "Could not read the list views Salesforce returned")
		return
	}

	options := make([]api.InputOption, 0, len(raw.ListViews))
	for _, lv := range raw.ListViews {
		if lv.ID == "" {
			continue
		}
		label := lv.Label
		if label == "" {
			label = lv.ID
		}
		options = append(options, api.InputOption{Name: label, Value: lv.ID})
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 11. Reports and report folders
// ---------------------------------------------------------------------------

// getSalesforceReports serves the org's reports for the Report input, or — with
// ?folders=true — the distinct folder names for the Report Folder filter. One
// endpoint, because both come from the same bounded query over the Report object.
func (s *Service) getSalesforceReports(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}
	foldersOnly := strings.EqualFold(strings.TrimSpace(c.Query("folders")), "true")

	soql := fmt.Sprintf("SELECT Id, Name, FolderName FROM Report ORDER BY FolderName, Name LIMIT %d",
		salesforceListLimit)
	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce reports")
		what := "list of reports"
		if foldersOnly {
			what = "list of report folders"
		}
		salesforceRespond(c, nil, salesforceErrorMessage(err, what))
		return
	}

	if foldersOnly {
		seen := map[string]struct{}{}
		options := make([]api.InputOption, 0, 16)
		for _, r := range records {
			folder := salesforceStringValue(r["FolderName"])
			if folder == "" {
				continue
			}
			if _, dup := seen[folder]; dup {
				continue
			}
			seen[folder] = struct{}{}
			options = append(options, api.InputOption{Name: folder, Value: folder})
		}
		salesforceSortOptions(options)
		salesforceRespond(c, options, "")
		return
	}

	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		id, _ := r["Id"].(string)
		if id == "" {
			continue
		}
		label := salesforceStringValue(r["Name"])
		if label == "" {
			label = id
		}
		// The folder is part of the label because report names repeat across
		// folders far more often than they are unique.
		if folder := salesforceStringValue(r["FolderName"]); folder != "" {
			label = folder + " / " + label
		}
		options = append(options, api.InputOption{Name: label, Value: id})
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}
