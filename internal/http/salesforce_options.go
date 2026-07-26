package http

// Live dropdowns for the CRM ▸ Salesforce actions.
//
// Salesforce is the node where pickers matter most. Every other CRM input is a
// name or a date; Salesforce inputs are RECORD IDS (0015f00000AbCdEAAV) and
// PICKLIST API NAMES that must match the org's setup exactly. A receptionist or
// sales admin — the person these flows are built for — cannot be asked to go and
// look either of those up. Fourteen proxies back all 565 markers registered from
// salesforce_options_markers.go:
//
//	/salesforce-objects                 the global describe (all / custom only)
//	/salesforce-fields                  one object's fields (all|createable|updateable|picklist|filterable|sortable)
//	/salesforce-picklist                one field's picklist values — EVERY picklist input
//	/salesforce-external-id-fields      fields usable as an upsert key
//	/salesforce-record-types            one object's active record types
//	/salesforce-lookup                  searchable record picker (one or more objects)
//	/salesforce-users                   active users (?owner=true: only those that can own a record)
//	/salesforce-owners                  users AND queues for one object
//	/salesforce-lead-converted-statuses the lead statuses that actually convert
//	/salesforce-contract-statuses       the contract statuses in one StatusCode
//	                                    category — the ones that actually activate,
//	                                    or the one a new contract can start in
//	/salesforce-campaign-member-status  one campaign's member statuses (two-hop)
//	/salesforce-list-views              one object's list views
//	/salesforce-reports                 reports, or the distinct report folders
//	/salesforce-price-book-entries      a price book's priced products (two-hop)
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
//  2. It never offers a choice the org would reject. Picklist values are filtered
//     on active == true (n8n returns entries retired in Setup); Sort By lists only
//     sortable fields and Filter Field only filterable ones (Salesforce answers
//     ORDER BY Description with MALFORMED_QUERY); Converted Status lists only the
//     statuses marked Converted, not the whole Lead.Status picklist; Activate
//     Contract lists only the statuses that ACTIVATE and Create Contract only the
//     one a contract can be created in, not all three of Contract.Status; and the
//     owner pickers drop the user types Salesforce refuses to make an owner.
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
	"context"
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
		MaxIdleConns: 20,
		// One polymorphic picker now fans out over up to salesforceMaxLookupObjects
		// objects at once, and each object is a describe followed by a query. At the
		// old cap of 4 the fifth connection of every burst was closed rather than
		// pooled, so the query round paid a fresh TLS handshake for it.
		MaxIdleConnsPerHost: salesforceMaxLookupObjects + 1,
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
	var noLabel *salesforceNoLabelFieldError
	if errors.As(err, &noLabel) {
		return fmt.Sprintf(
			"Salesforce doesn't publish a name for %s records, so they can't be listed here — type the record ID in",
			noLabel.object)
	}
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
	Name  string
	Label string
	Type  string
	// RelationshipName is the name a SOQL parent traversal uses — AccountId's is
	// "Account", so "Account.Name" reaches the parent's name. It is captured only
	// so a traversed secondary label column can be CONFIRMED against the describe
	// before it goes into a SELECT (see salesforceResolveSecondaryLabel).
	RelationshipName string
	Createable       bool
	Updateable       bool
	Filterable       bool
	Sortable         bool
	ExternalID       bool
	IDLookup         bool
	NameField        bool
	PicklistValues   []api.InputOption
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

// The in-flight table. The cache alone does not stop duplicate work: an entry is
// only written once a describe has COME BACK, so nothing prevents N goroutines
// being partway through the same describe at once — and that is the normal case,
// not a rare race. The editor fetches every dynamic option on mount, so opening
// one Salesforce node fires all of its pickers simultaneously; task_update alone
// describes Task eight times over, ~120 KB each, and every one of them counts
// against the customer's daily API allowance.
type salesforceInFlight struct {
	done  chan struct{}
	value any
	err   error
}

var (
	salesforceInFlightMu    sync.Mutex
	salesforceInFlightCalls = map[string]*salesforceInFlight{}
)

// salesforceFetchOnce collapses concurrent misses on one cache key into a single
// upstream call.
//
// A follower NEVER inherits the leader's error. Every outbound call is bound to
// its own request's context, so the leader failing may mean nothing worse than
// "that operator closed the config panel" — sharing that failure would blank
// six other perfectly live dropdowns. On failure each waiter simply does the
// work itself, which is exactly the behaviour that existed before this function.
func salesforceFetchOnce(ctx context.Context, key string, fetch func() (any, error)) (any, error) {
	salesforceInFlightMu.Lock()
	if call, waiting := salesforceInFlightCalls[key]; waiting {
		salesforceInFlightMu.Unlock()
		select {
		case <-call.done:
			if call.err == nil {
				return call.value, nil
			}
			return fetch()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &salesforceInFlight{done: make(chan struct{})}
	salesforceInFlightCalls[key] = call
	salesforceInFlightMu.Unlock()

	call.value, call.err = fetch()

	salesforceInFlightMu.Lock()
	delete(salesforceInFlightCalls, key)
	salesforceInFlightMu.Unlock()
	close(call.done)

	return call.value, call.err
}

// salesforceDescribeObject fetches (or serves from cache) one object's describe.
func salesforceDescribeObject(c *gin.Context, conn salesforceProxyConn, object string) (*salesforceDescribe, error) {
	key := "sobject\x00" + conn.Fingerprint + "\x00" + object
	if cached, ok := salesforceCacheGet(key); ok {
		if d, ok := cached.(*salesforceDescribe); ok {
			return d, nil
		}
	}

	value, err := salesforceFetchOnce(c.Request.Context(), key, func() (any, error) {
		return salesforceFetchDescribe(c, conn, object, key)
	})
	if err != nil {
		return nil, err
	}
	describe, ok := value.(*salesforceDescribe)
	if !ok {
		return nil, fmt.Errorf("salesforce describe of %s returned an unexpected shape", object)
	}
	return describe, nil
}

// salesforceFetchDescribe is the uncached describe. It is only ever entered
// through salesforceFetchOnce.
func salesforceFetchDescribe(c *gin.Context, conn salesforceProxyConn, object, key string) (*salesforceDescribe, error) {
	body, err := salesforceGet(c, conn, "/sobjects/"+url.PathEscape(object)+"/describe")
	if err != nil {
		return nil, err
	}

	var raw struct {
		Fields []struct {
			Name             string `json:"name"`
			Label            string `json:"label"`
			Type             string `json:"type"`
			RelationshipName string `json:"relationshipName"`
			Createable       bool   `json:"createable"`
			Updateable       bool   `json:"updateable"`
			Filterable       bool   `json:"filterable"`
			Sortable         bool   `json:"sortable"`
			ExternalID       bool   `json:"externalId"`
			IDLookup         bool   `json:"idLookup"`
			NameField        bool   `json:"nameField"`
			PicklistValues   []struct {
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
			RelationshipName: f.RelationshipName,
			Createable:       f.Createable, Updateable: f.Updateable,
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

	value, err := salesforceFetchOnce(c.Request.Context(), key, func() (any, error) {
		return salesforceFetchGlobalDescribe(c, conn, key)
	})
	if err != nil {
		return nil, err
	}
	objects, ok := value.([]salesforceGlobalObject)
	if !ok {
		return nil, errors.New("salesforce global describe returned an unexpected shape")
	}
	return objects, nil
}

// salesforceFetchGlobalDescribe is the uncached global describe. It is only ever
// entered through salesforceFetchOnce.
func salesforceFetchGlobalDescribe(c *gin.Context, conn salesforceProxyConn, key string) ([]salesforceGlobalObject, error) {
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

	// The parent set is built from EVERY name the describe returned, not just the
	// queryable ones, so a shadow table whose parent the user cannot query is
	// still recognised as a shadow table.
	known := make(map[string]bool, len(raw.SObjects))
	for _, o := range raw.SObjects {
		known[o.Name] = true
	}

	objects := make([]salesforceGlobalObject, 0, len(raw.SObjects))
	for _, o := range raw.SObjects {
		if !o.Queryable || o.DeprecatedAndHidden {
			continue
		}
		if salesforceIsSystemObject(o.Name, o.Custom, known) {
			continue
		}
		objects = append(objects, salesforceGlobalObject{Name: o.Name, Label: o.Label, Custom: o.Custom})
	}

	salesforceCachePut(key, objects)
	return objects, nil
}

// salesforceIsSystemObject hides the shadow objects every org carries — the
// per-object Share / History / Feed / Tag / ChangeEvent tables. They are a third
// of the global describe and none of them is ever what an operator meant.
//
// The bare (un-prefixed) suffixes need two extra tests, because on their own they
// also hide real standard objects:
//
//   - Custom objects are exempt, so a genuine "TimeShare__c" survives.
//   - A shadow table is always named <Parent><Suffix> for a <Parent> the same
//     global describe lists, so the bare suffixes only bite when that parent is
//     really there. LoginHistory ("Login History") and VerificationHistory
//     ("Identity Verification History") are queryable, non-deprecated standard
//     objects with no Login or Verification object behind them — verified live —
//     and a bare "History" rule drops both, leaving an operator who wants login
//     history to conclude Flomation cannot read it.
//
// The one shadow family that does NOT trim to its parent is field history:
// OpportunityFieldHistory trims to "OpportunityField", which is not an object.
// It is matched by name instead, or the parent test would surface it.
//
// known is the set of names in the same global describe; a nil set means "no
// parent information", which errs towards showing the object.
func salesforceIsSystemObject(name string, custom bool, known map[string]bool) bool {
	for _, suffix := range salesforceSystemObjectSuffixes {
		if !strings.HasSuffix(name, suffix) || len(name) <= len(suffix) {
			continue
		}
		// Namespaced shadow tables (Invoice__c__History) and change events are
		// unambiguous — nothing else is named that way.
		if strings.HasPrefix(suffix, "__") || suffix == "ChangeEvent" {
			return true
		}
		if custom {
			continue
		}
		if strings.HasSuffix(name, "FieldHistory") && len(name) > len("FieldHistory") {
			return true
		}
		return known[strings.TrimSuffix(name, suffix)]
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
	"filterable": {}, "sortable": {},
}

// salesforceDistanceOnlyTypes are the compound types whose describe reports
// filterable:true and whose SOQL then refuses an ordinary comparison —
// "Address fields can only be filtered using Distance expressions" (verified
// live on Account.BillingAddress). The Filterable flag alone is therefore not a
// sufficient test for the Filter Field / Look Up By pickers.
var salesforceDistanceOnlyTypes = map[string]struct{}{
	"address":  {},
	"location": {}, // geolocation compound fields have the same DISTANCE-only rule
}

// getSalesforceFields serves one object's fields for the Fields / Filter Field /
// Sort By / Field to Set / Look Up By / Dropdown Field inputs.
//
// The filter is not cosmetic. Salesforce refuses to filter or sort on some of
// the fields its own describe returns — ORDER BY Description is MALFORMED_QUERY
// "field 'Description' can not be sorted in a query call", WHERE Description is
// INVALID_FIELD — so a Sort By list built from every field offers choices that
// are always an error, with nothing on the option to say which ones. That is the
// same defect as the retired picklist value, one endpoint along.
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
		case "filterable":
			if !f.Filterable {
				continue
			}
			if _, distanceOnly := salesforceDistanceOnlyTypes[f.Type]; distanceOnly {
				continue
			}
		case "sortable":
			if !f.Sortable {
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

	// The objects are searched CONCURRENTLY. Each one costs a describe plus a
	// query, and email_send's related_record_id names five — ten round trips one
	// after another, measured at 2.7 s cold against a bare Developer Edition org
	// and worse on a real one. The loop body was already independent and already
	// tolerated a per-object failure, so the only thing sequence bought was
	// latency. Results are collected per index rather than appended, so the
	// output does not depend on which object answered first.
	type lookupResult struct {
		rows []api.InputOption
		err  error
	}
	results := make([]lookupResult, len(objects))
	var wg sync.WaitGroup
	for i, object := range objects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := salesforceLookupOne(c, conn, object, search, len(objects) > 1)
			results[i] = lookupResult{rows: rows, err: err}
		}()
	}
	wg.Wait()

	options := make([]api.InputOption, 0, salesforceLookupLimit)
	var lastErr error
	for i, res := range results {
		if res.err != nil {
			// One object of a polymorphic set failing (a Lead the user cannot see,
			// say) must not blank the whole picker.
			lastErr = res.err
			log.WithFields(log.Fields{"error": res.err, "object": objects[i]}).Warn("unable to search Salesforce records")
			continue
		}
		options = append(options, res.rows...)
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

	// Find the object's own name field. The override comes first, then the
	// describe's own nameField flag (Salesforce flags exactly one), then the
	// fallbacks that cover the handful of objects flagging none.
	var nameField *salesforceDescribeField
	if candidate, overridden := salesforceLookupNameField[object]; overridden {
		for i := range describe.Fields {
			if strings.EqualFold(describe.Fields[i].Name, candidate) {
				nameField = &describe.Fields[i]
				break
			}
		}
	}
	if nameField == nil {
		for i := range describe.Fields {
			if describe.Fields[i].NameField {
				nameField = &describe.Fields[i]
				break
			}
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
		// Not INVALID_FIELD: nothing is wrong with the operator's field, the
		// object simply has no column worth showing, and telling them "Salesforce
		// doesn't have that field on this object" sends them looking for a
		// mistake they did not make.
		return nil, &salesforceNoLabelFieldError{object: object}
	}

	// A second column is selected when the name alone does not identify the
	// record — a Case's number means nothing without its subject, and a Contract's
	// nothing without the customer's name. Only ever a column the describe
	// actually reports, so this cannot produce INVALID_FIELD.
	var secondary salesforceSecondaryLabel
	if candidate, wanted := salesforceLookupSecondaryField[object]; wanted {
		secondary = salesforceResolveSecondaryLabel(describe, candidate)
	}

	selectList := "Id, " + nameField.Name
	if secondary.Path != "" {
		selectList += ", " + secondary.Path
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
		if secondary.Path != "" {
			if extra := secondary.value(r); extra != "" {
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

// salesforceLookupNameField names the label column for the objects whose
// describe flags NO nameField and carries none of the generic fallbacks either.
//
// OrgWideEmailAddress is the one such object any marker points at. Its fields are
// Id, CreatedById, CreatedDate, LastModifiedDate, LastModifiedById,
// SystemModstamp, IsVerified, Address, DisplayName, IsAllowAllProfiles and
// Purpose — standard schema, so this is true of every org, and without the
// override the "Org-Wide Email Address" picker on email_send can never return a
// single option no matter how the org is set up.
var salesforceLookupNameField = map[string]string{
	"OrgWideEmailAddress": "DisplayName",
}

// salesforceLookupSecondaryField names a second column worth showing beside the
// object's name field, for the objects whose name is an opaque reference. A value
// containing a dot is ONE parent traversal (see salesforceResolveSecondaryLabel).
var salesforceLookupSecondaryField = map[string]string{
	"Case":                "Subject", // CaseNumber alone tells the operator nothing
	"OrgWideEmailAddress": "Address", // "Support" means nothing without support@acme.com
	// Contract and Order have NOTHING on themselves worth showing: their name
	// field is an auto-number — verified live, Contract's is ContractNumber
	// ("00000109") and Order's is OrderNumber ("00000114") — so a picker built
	// from it alone asks a receptionist to choose between six-digit numbers. The
	// customer is the only thing that identifies either record and it lives one
	// relationship away. Contract.AccountId is not even nillable, so the traversal
	// always resolves; Order's is, and an order with no account simply shows its
	// number, which is what it had before.
	"Contract": "Account.Name",
	"Order":    "Account.Name",
	// Assets repeat by design — forty "Dell Latitude 5540" rows under different
	// customers — and the serial number is what tells them apart. Same-object, and
	// only appended when the asset actually carries one.
	"Asset": "SerialNumber",
}

// salesforceSecondaryLabel is a resolved secondary label column: the SELECT path,
// plus what it takes to read the value back out of a record.
type salesforceSecondaryLabel struct {
	// Path is what goes in the SELECT list ("Subject", or "Account.Name").
	Path string
	// Relationship and Leaf are set only for a traversal. Salesforce returns a
	// traversed column NESTED — {"Account": {"Name": "Acme Ltd"}} — so the flat
	// record[Path] lookup a same-object column uses finds nothing.
	Relationship string
	Leaf         string
}

// value reads the column out of one SOQL record.
func (s salesforceSecondaryLabel) value(record map[string]any) string {
	if s.Relationship == "" {
		return salesforceStringValue(record[s.Path])
	}
	nested, isMap := record[s.Relationship].(map[string]any)
	if !isMap {
		// A null lookup comes back as JSON null, not an empty object.
		return ""
	}
	return salesforceStringValue(nested[s.Leaf])
}

// salesforceResolveSecondaryLabel resolves a salesforceLookupSecondaryField entry
// against the object's describe. Two forms are accepted:
//
//   - "Subject"      — a field on the object itself.
//   - "Account.Name" — one hop across a lookup, for the objects whose own name
//     field is an auto-number.
//
// NOTHING is returned unless the describe confirms the field, or the
// relationship, so a stale entry in the table degrades to "no second column"
// rather than to an INVALID_FIELD on every dropdown open. The traversal is capped
// at one hop deliberately: two hops is a different query-planning risk and no
// picker needs it.
func salesforceResolveSecondaryLabel(describe *salesforceDescribe, candidate string) salesforceSecondaryLabel {
	candidate = strings.TrimSpace(candidate)
	// The table is ours and compile-time, but it feeds a SELECT list that cannot
	// quote an identifier, so it is whitelisted like every other identifier here.
	if candidate == "" || !salesforceFieldPattern.MatchString(candidate) {
		return salesforceSecondaryLabel{}
	}
	relationship, leaf, traversed := strings.Cut(candidate, ".")
	if !traversed {
		for _, f := range describe.Fields {
			if strings.EqualFold(f.Name, candidate) {
				return salesforceSecondaryLabel{Path: f.Name}
			}
		}
		return salesforceSecondaryLabel{}
	}
	if strings.Contains(leaf, ".") {
		return salesforceSecondaryLabel{} // more than one hop
	}
	for _, f := range describe.Fields {
		if f.RelationshipName != "" && strings.EqualFold(f.RelationshipName, relationship) {
			// The describe's own casing, so the nested key the response comes back
			// under matches what is read out of it.
			return salesforceSecondaryLabel{
				Path:         f.RelationshipName + "." + leaf,
				Relationship: f.RelationshipName,
				Leaf:         leaf,
			}
		}
	}
	return salesforceSecondaryLabel{}
}

// salesforceNoLabelFieldError is the one failure salesforceLookupOne raises
// itself: the object has nothing that can be shown as a label, so there is no
// picker to build. It is deliberately NOT a salesforceStatusError — Salesforce
// never said no, and dressing it as INVALID_FIELD tells the operator a field is
// missing on an input where nothing they can do would ever populate the list.
type salesforceNoLabelFieldError struct{ object string }

func (e *salesforceNoLabelFieldError) Error() string {
	return "salesforce object " + e.object + " has no field usable as a label"
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
//
// ?owner=true narrows it further to the users that can actually OWN a record.
// It is set on the owner_id markers only: user_id on user_get / user_update /
// user_deactivate legitimately names a Chatter Free user, and hiding those would
// be the same defect the other way round.
func (s *Service) getSalesforceUsers(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	options, err := salesforceFetchUsers(c, conn, salesforceUserQuery{
		IncludeInactive: strings.EqualFold(strings.TrimSpace(c.Query("include_inactive")), "true"),
		OwnersOnly:      strings.EqualFold(strings.TrimSpace(c.Query("owner")), "true"),
	})
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce users")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of users"))
		return
	}
	salesforceSortOptions(options)
	salesforceRespond(c, options, "")
}

// salesforceNonOwnerUserTypes are the user types Salesforce refuses as an
// OwnerId. A Chatter Free (CsnOnly) or Chatter External (CsnExternal) user has
// no record-ownership licence, so the insert comes back
// OP_WITH_INVALID_USER_TYPE_EXCEPTION "Operation not valid for this user type" —
// verified live on Lead, Task and Event. Every org ships one of these (the stock
// "Chatter Expert"), so without this predicate EVERY customer's owner picker
// carries at least one row that is guaranteed to fail.
//
// This is an exclude list and not a `UserType = 'Standard'` whitelist on
// purpose. AutomatedProcess, CloudIntegrationUser and the portal types are all
// accepted as owners — I probed each one live and Salesforce created the record —
// and a partner-community user owning a registered deal is ordinary in a PRM
// org. Whitelisting Standard would turn an over-permissive picker into an
// under-permissive one, which is the same harm wearing a different hat.
var salesforceNonOwnerUserTypes = []string{"CsnOnly", "CsnExternal"}

// salesforceUserQuery is how a caller narrows the user list. It is a struct
// rather than a run of bare booleans so the call sites say which is which.
type salesforceUserQuery struct {
	IncludeInactive bool
	OwnersOnly      bool
	// Prefix, when set, is put in front of every label (the owners picker labels
	// its user group).
	Prefix string
}

// salesforceFetchUsers runs the bounded user query.
func salesforceFetchUsers(c *gin.Context, conn salesforceProxyConn, q salesforceUserQuery) ([]api.InputOption, error) {
	var where []string
	if !q.IncludeInactive {
		where = append(where, "IsActive = true")
	}
	if q.OwnersOnly {
		quoted := make([]string, 0, len(salesforceNonOwnerUserTypes))
		for _, t := range salesforceNonOwnerUserTypes {
			quoted = append(quoted, "'"+t+"'")
		}
		where = append(where, "UserType NOT IN ("+strings.Join(quoted, ",")+")")
	}

	soql := "SELECT Id, Name, Username FROM User"
	if len(where) > 0 {
		soql += " WHERE " + strings.Join(where, " AND ")
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
		options = append(options, api.InputOption{Name: q.Prefix + label, Value: id})
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

	users, err := salesforceFetchUsers(c, conn, salesforceUserQuery{OwnersOnly: true, Prefix: "User: "})
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
// 9. Lead statuses that actually convert
// ---------------------------------------------------------------------------

// getSalesforceLeadConvertedStatuses serves the lead statuses an administrator
// has ticked "Converted" in Setup — the only values lead_convert accepts.
//
// It exists because the obvious endpoint is wrong. The Lead.Status PICKLIST holds
// every status (in a default org: Open - Not Contacted, Working - Contacted,
// Closed - Converted, Closed - Not Converted) and three of those four come back
// from convertLead as INVALID_STATUS "invalid convertedStatus" — verified live.
// "Closed - Not Converted" is exactly the one an operator recording a dead lead
// would reach for. Which statuses convert lives on the LeadStatus object, not in
// the picklist, so only a query can tell them apart. Same reasoning as the
// per-campaign member statuses below: when the generic picker would offer
// choices the action rejects, the picker gets built properly instead.
func (s *Service) getSalesforceLeadConvertedStatuses(c *gin.Context) {
	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	soql := fmt.Sprintf(
		"SELECT MasterLabel, IsDefault FROM LeadStatus WHERE IsConverted = true ORDER BY SortOrder LIMIT %d",
		salesforceListLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithField("error", err).Warn("unable to list Salesforce converted lead statuses")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of converted statuses"))
		return
	}

	// convertLead takes the status LABEL, not an id, so the label is both what the
	// operator sees and what the flow sends.
	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		label := salesforceStringValue(r["MasterLabel"])
		if label == "" {
			continue
		}
		name := label
		if isDefault, _ := r["IsDefault"].(bool); isDefault {
			name += " (default)"
		}
		options = append(options, api.InputOption{Name: name, Value: label})
	}
	if len(options) == 0 {
		salesforceRespond(c, nil, "No lead status in your org is marked as Converted — ask your Salesforce administrator to tick Converted on one under Setup ▸ Lead Status")
		return
	}
	// NOT sorted: SortOrder is the sequence the org's own setup defines.
	salesforceRespond(c, options, "")
}

// ---------------------------------------------------------------------------
// 10. Campaign member statuses (two-hop)
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
// 11. List views
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
// 12. Reports and report folders
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

// ---------------------------------------------------------------------------
// 13. Price book entries (two-hop)
// ---------------------------------------------------------------------------

// A price book ENTRY is the one commerce id an operator has no way to reach. It
// is the join between a price book and a product — "GenWatt Diesel 200kW costs
// £25,000 on the Standard book" — and it is what Salesforce demands before a
// product can go on a quote, an order or an opportunity. The operator has the
// price book and the product in front of them and neither is the id the API wants.
//
// The generic /salesforce-lookup CANNOT do this job, and the reason is concrete
// rather than theoretical. PricebookEntry's describe flags Name as its name field,
// and that Name is the PRODUCT's name, repeated once per book the product is
// priced in. Verified against the live org (17 products, 2 price books,
// 34 entries), SELECT Id, Name FROM PricebookEntry ORDER BY Name returns:
//
//	01uaj000008Qi5mAAC  GenWatt Diesel 1000kW
//	01uaj000008Qi63AAC  GenWatt Diesel 1000kW
//	01uaj000008Qi5fAAC  GenWatt Diesel 10kW
//	01uaj000008Qi5wAAC  GenWatt Diesel 10kW
//
// — every row twice, with nothing on the option to say which book it belongs to.
// A picker that offers the same words twice and rejects one of them is worse than
// no picker at all.
//
// So this endpoint labels an entry with all three facts the operator recognises
// (book, product, price) and, where the action gives it something to go on, scopes
// the list to the ONE book that can legally be used:
//
//	?scope=quote        + quote_id        → the quote's price book
//	?scope=order        + order_id        → the order's price book
//	?scope=opportunity  + opportunity_id  → the opportunity's price book
//	(no scope)                            → every book, each row prefixed
//
// The scope is the same shape as the campaign-member-status picker: a sibling
// input the editor forwards, one extra query to resolve it, and a plain sentence
// asking for it when it is not there yet. It matters more here than as a nicety —
// a line item's PricebookEntry MUST belong to its parent's price book, so an
// unscoped list on a quote would be mostly rows Salesforce refuses.
//
// ?include_inactive=true keeps the retired entries in, which the Change Product
// Price action needs: an operator reactivating a discontinued price cannot pick
// the row if the picker has filtered it out. Everywhere else they are dropped —
// adding an inactive entry to a quote is refused.

// salesforcePriceBookEntryScope is one parent record a price book entry list can
// be scoped by: the sibling input carrying its id, the object to read, and the
// word to use when asking the operator for it.
type salesforcePriceBookEntryScope struct {
	Param  string
	Object string
	What   string
}

// salesforcePriceBookEntryScopes is the closed set of ?scope= values. Like
// ?filter= on the fields picker, the scope is OUR parameter — baked into the
// marker endpoint, never typed by an operator — so anything outside this set is a
// bug or a hand-crafted request.
var salesforcePriceBookEntryScopes = map[string]salesforcePriceBookEntryScope{
	"quote":       {Param: "quote_id", Object: "Quote", What: "quote"},
	"order":       {Param: "order_id", Object: "Order", What: "order"},
	"opportunity": {Param: "opportunity_id", Object: "Opportunity", What: "opportunity"},
}

// getSalesforcePriceBookEntries serves the priced products of a price book.
func (s *Service) getSalesforcePriceBookEntries(c *gin.Context) {
	scopeName := strings.TrimSpace(c.Query("scope"))
	scope, scoped := salesforcePriceBookEntryScopes[scopeName]
	if scopeName != "" && !scoped {
		salesforceRespond(c, nil, "Unknown price book scope")
		return
	}

	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	// Hop one: the parent record names the only price book that can be used.
	pricebookID := ""
	if scoped {
		parentID := strings.TrimSpace(c.Query(scope.Param))
		if parentID == "" || strings.HasPrefix(parentID, "${") {
			salesforceRespond(c, nil, fmt.Sprintf(
				"Choose the %s first — the products you can add come from its price book", scope.What))
			return
		}
		if !salesforceIDPattern.MatchString(parentID) {
			salesforceRespond(c, nil, fmt.Sprintf(
				"That %s ID doesn't look like a Salesforce record ID — choose the %s from the list", scope.What, scope.What))
			return
		}
		resolved, errMsg := salesforceParentPriceBook(c, conn, scope, parentID)
		if errMsg != "" {
			salesforceRespond(c, nil, errMsg)
			return
		}
		// A parent with no price book yet is not an error: Salesforce sets the
		// quote's or order's book FROM its first line item, so at that moment every
		// active entry is a legal choice. The list widens to all books and each row
		// says which book it is from, rather than the picker refusing to load.
		pricebookID = resolved
	}
	// An explicit price book narrows the list on its own, and also takes over when
	// the parent has not settled on one.
	if pricebookID == "" {
		if raw := strings.TrimSpace(c.Query("pricebook_id")); raw != "" && !strings.HasPrefix(raw, "${") &&
			salesforceIDPattern.MatchString(raw) {
			pricebookID = raw
		}
	}

	// Hop two: the entries themselves. The describe is needed for one thing only —
	// whether this org has currencies at all. CurrencyIsoCode does not exist on a
	// single-currency org and selecting it is a hard INVALID_FIELD (verified live:
	// "No such column 'CurrencyIsoCode' on entity 'PricebookEntry'"), while in a
	// multi-currency org a bare "100000" beside a product does not say which
	// currency it is in.
	describe, err := salesforceDescribeObject(c, conn, "PricebookEntry")
	if err != nil {
		log.WithField("error", err).Warn("unable to describe PricebookEntry for the price book entry picker")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of priced products"))
		return
	}
	currencyField := ""
	for _, f := range describe.Fields {
		if strings.EqualFold(f.Name, "CurrencyIsoCode") {
			currencyField = f.Name
			break
		}
	}

	selectList := "Id, Name, UnitPrice, IsActive, Pricebook2.Name, Product2.Name, Product2.ProductCode"
	if currencyField != "" {
		selectList += ", " + currencyField
	}

	var where []string
	if !strings.EqualFold(strings.TrimSpace(c.Query("include_inactive")), "true") {
		// The same rule as the retired picklist value and the deactivated user: an
		// inactive entry is offered by Salesforce and then refused on write.
		where = append(where, "IsActive = true")
	}
	if pricebookID != "" {
		where = append(where, "Pricebook2Id = '"+salesforceLiteralEscaper.Replace(pricebookID)+"'")
	}
	// A product narrows the list to the same product's price in each book, which is
	// what "Add Product to Quote" wants once the product is chosen. An unresolved
	// binding is IGNORED rather than refused — it is an optional narrowing, and
	// blanking the whole picker over it would be the worse failure.
	if raw := strings.TrimSpace(c.Query("product_id")); raw != "" && !strings.HasPrefix(raw, "${") &&
		salesforceIDPattern.MatchString(raw) {
		where = append(where, "Product2Id = '"+salesforceLiteralEscaper.Replace(raw)+"'")
	}
	// The search term is the only free-text value in this statement. It goes inside
	// a quoted LIKE literal with the wildcards escaped, against the PRODUCT's name
	// — the words the operator is actually typing (verified live against the org).
	if search := strings.TrimSpace(c.Query("search")); search != "" && !strings.HasPrefix(search, "${") {
		where = append(where, "Product2.Name LIKE '%"+salesforceLikeEscaper.Replace(search)+"%'")
	}

	soql := "SELECT " + selectList + " FROM PricebookEntry"
	if len(where) > 0 {
		soql += " WHERE " + strings.Join(where, " AND ")
	}
	// Book then product, which is the order the labels read in, and bounded like
	// every other record picker here.
	soql += fmt.Sprintf(" ORDER BY Pricebook2.Name, Name LIMIT %d", salesforceLookupLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "pricebook": pricebookID}).
			Warn("unable to list Salesforce price book entries")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of priced products"))
		return
	}

	// The book is named on every row unless the list is already ONE book, where
	// repeating it on all hundred rows is just noise.
	prefixBook := pricebookID == ""

	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		id, _ := r["Id"].(string)
		if id == "" {
			continue
		}
		product, code := "", ""
		if nested, isMap := r["Product2"].(map[string]any); isMap {
			product = salesforceStringValue(nested["Name"])
			code = salesforceStringValue(nested["ProductCode"])
		}
		if product == "" {
			// PricebookEntry.Name mirrors the product's name, so it is the same
			// string by another route when the traversal came back empty.
			product = salesforceStringValue(r["Name"])
		}
		if product == "" {
			product = id
		}
		label := product
		if code != "" {
			label += " (" + code + ")"
		}
		if price := salesforceStringValue(r["UnitPrice"]); price != "" {
			if currencyField != "" {
				if currency := salesforceStringValue(r[currencyField]); currency != "" {
					price += " " + currency
				}
			}
			label += " — " + price
		}
		if active, isBool := r["IsActive"].(bool); isBool && !active {
			// Only reachable with ?include_inactive=true, and the operator reaching
			// for a retired price needs to see which rows those are.
			label += " (inactive)"
		}
		if prefixBook {
			book := ""
			if nested, isMap := r["Pricebook2"].(map[string]any); isMap {
				book = salesforceStringValue(nested["Name"])
			}
			if book != "" {
				label = book + ": " + label
			}
		}
		options = append(options, api.InputOption{Name: label, Value: id})
	}
	// NOT re-sorted: the ORDER BY already delivers book-then-product, and sorting
	// the composite label instead would put "£100" before "£25" by spelling.
	salesforceRespond(c, options, "")
}

// salesforceParentPriceBook reads the price book off the quote / order /
// opportunity a line-item action is adding to. An empty id with an empty message
// means the parent simply has no price book yet, which is a legitimate state.
func salesforceParentPriceBook(c *gin.Context, conn salesforceProxyConn, scope salesforcePriceBookEntryScope, parentID string) (string, string) {
	// The object comes from OUR closed scope table, never from the request, and the
	// id has already been matched against the record-id pattern — so the only
	// caller-influenced value here is an 18-character alphanumeric, escaped anyway.
	soql := "SELECT Pricebook2Id FROM " + scope.Object + " WHERE Id = '" +
		salesforceLiteralEscaper.Replace(parentID) + "' LIMIT 1"

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "object": scope.Object, "record": parentID}).
			Warn("unable to read the parent price book for the price book entry picker")
		return "", salesforceErrorMessage(err, "price book for that "+scope.What)
	}
	if len(records) == 0 {
		return "", fmt.Sprintf(
			"That %s isn't in Salesforce any more — choose it again to list the products you can add", scope.What)
	}
	return salesforceStringValue(records[0]["Pricebook2Id"]), ""
}

// ---------------------------------------------------------------------------
// 14. Contract statuses, by what the status actually DOES
// ---------------------------------------------------------------------------

// Contract.Status is a restricted picklist of labels an administrator can rename,
// and behind each label sits a StatusCode — the category Salesforce itself acts
// on. Two contract actions accept exactly one category, and the generic picklist
// offers all three of them:
//
//   - Activate Contract writes the status that makes a contract live. Verified
//     live: PATCHing Status "Draft" or "In Approval Process" onto a draft
//     contract answers 204 and leaves ActivatedDate null. Salesforce takes the
//     write; the contract is simply not activated. Offering those two rows on a
//     step called Activate Contract is a one-click way to have a flow report an
//     activation that never happened and carry on down its success branch.
//   - Create Contract can only insert a DRAFT. Verified live: POSTing Status
//     "In Approval Process" or "Activated" is 400 FAILED_ACTIVATION ("Choose a
//     valid contract status and save your changes"), so two of the three rows the
//     generic picklist offers fail every single time.
//
// Update Contract and Get Many Contracts keep the full picklist: every status is
// a legitimate thing to move a contract TO (verified live, Draft → In Approval
// Process → Activated all answer 204) and every status is a legitimate thing to
// filter a report BY.
//
// Which category a label belongs to is NOT in the field describe — it is on the
// ContractStatus object — so, exactly as with the converted lead statuses above,
// the picker gets built properly rather than pointed at the picklist. Same rule:
// when the generic picker would offer choices the action rejects, or quietly
// fails to honour, it is the wrong picker.
//
// The VALUE served still comes from the field describe rather than from
// ContractStatus. The write lands on Contract.Status, which is restricted, so the
// only string guaranteed to be accepted is that picklist's own value — and in an
// org that has RENAMED a status (the case these inputs exist for) the label and
// the API name are no longer the same string. So ContractStatus decides which
// rows are offered and the describe decides what each row sends. A status retired
// in Setup drops out for free: it is still a ContractStatus row but no longer an
// active picklist value.

// salesforceContractStatusCode is one StatusCode category a marker can ask for.
type salesforceContractStatusCode struct {
	// Code is the SOQL literal. It comes from this table's key and never from the
	// request, which is what keeps it out of the escaping question.
	Code string
	// Empty is what the operator reads when the org has no live status in this
	// category. Naming the Setup page is the whole value of the message: nothing
	// they can do in the flow will populate the list.
	Empty string
}

// salesforceContractStatusCodes is the closed set of ?status_code= values. Like
// ?filter= and ?scope=, it is OUR parameter — baked into the marker endpoint,
// never typed by an operator — so anything outside this set is a bug or a
// hand-crafted request.
var salesforceContractStatusCodes = map[string]salesforceContractStatusCode{
	"Activated": {
		Code:  "Activated",
		Empty: "No status in your org's Contract Status list activates a contract — ask your Salesforce administrator which status under Setup ▸ Contract Status has the Activated code, then type it here",
	},
	"Draft": {
		Code:  "Draft",
		Empty: "No status in your org's Contract Status list can start a new contract — ask your Salesforce administrator which status under Setup ▸ Contract Status has the Draft code, then type it here",
	},
}

// getSalesforceContractStatuses serves the contract statuses in one StatusCode
// category: the only values Activate Contract and Create Contract can be given
// and still do what their names say.
func (s *Service) getSalesforceContractStatuses(c *gin.Context) {
	requested := strings.TrimSpace(c.Query("status_code"))
	wanted, known := salesforceContractStatusCodes[requested]
	if !known {
		salesforceRespond(c, nil, "Unknown contract status category")
		return
	}

	conn, ok := s.salesforceConn(c)
	if !ok {
		return
	}

	// ApiName as well as MasterLabel: a renamed status has one of each, and either
	// may be the string the field's picklist carries.
	soql := fmt.Sprintf(
		"SELECT MasterLabel, ApiName, IsDefault FROM ContractStatus WHERE StatusCode = '%s' ORDER BY SortOrder LIMIT %d",
		wanted.Code, salesforceListLimit)

	records, err := salesforceQuery(c, conn, soql)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "status_code": wanted.Code}).
			Warn("unable to list Salesforce contract statuses")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of contract statuses"))
		return
	}

	// The describe is the authority on what Contract.Status will accept, and it has
	// already dropped the values retired in Setup.
	describe, err := salesforceDescribeObject(c, conn, "Contract")
	if err != nil {
		log.WithField("error", err).Warn("unable to describe Contract for the contract status picker")
		salesforceRespond(c, nil, salesforceErrorMessage(err, "list of contract statuses"))
		return
	}
	var picklist []api.InputOption
	for _, f := range describe.Fields {
		if strings.EqualFold(f.Name, "Status") {
			picklist = f.PicklistValues
			break
		}
	}

	options := make([]api.InputOption, 0, len(records))
	for _, r := range records {
		value, matched := salesforceMatchPicklistValue(picklist,
			salesforceStringValue(r["ApiName"]), salesforceStringValue(r["MasterLabel"]))
		if !matched {
			continue
		}
		if isDefault, _ := r["IsDefault"].(bool); isDefault {
			value.Name += " (default)"
		}
		options = append(options, value)
	}
	if len(options) == 0 {
		salesforceRespond(c, nil, wanted.Empty)
		return
	}
	// NOT sorted: SortOrder is the sequence the org's own setup defines, and these
	// lists are one or two rows anyway.
	salesforceRespond(c, options, "")
}

// salesforceMatchPicklistValue finds the picklist value one of a set of candidate
// strings names, and returns it as the option to serve — label from the describe,
// value from the describe. Candidates are tried in order and each is matched
// against both sides, because a renamed status has a MasterLabel that matches the
// picklist's LABEL and an ApiName that matches its VALUE, and which of the two a
// given org kept unchanged is not knowable here.
func salesforceMatchPicklistValue(values []api.InputOption, candidates ...string) (api.InputOption, bool) {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, v := range values {
			if strings.EqualFold(v.Value, candidate) || strings.EqualFold(v.Name, candidate) {
				return v, true
			}
		}
	}
	return api.InputOption{}, false
}
