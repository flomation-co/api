package http

// Tests for the CRM ▸ Salesforce dropdown proxies (salesforce_options.go) and
// their 429 markers (salesforce_options_markers.go).
//
// The properties worth a regression test — the ones that are silent when broken:
//
//  1. EVERY input failure is HTTP 200 + {"error": …}, never a 4xx/5xx, because the
//     editor renders the message inline and the input falls back to manual entry.
//     A 4xx here is a dropdown that spins forever.
//  2. instance_url is caller-supplied and BECOMES the request host, so a crafted
//     value must be refused before the access token is ever attached. The tests
//     that matter assert no request LEFT THE PROCESS, not merely that the response
//     said no.
//  3. The describe cache is keyed on the credential. Describe output is filtered by
//     the connected user's field-level security, so a credential-blind key serves
//     one user's visible fields to another. The test proves this by handing two
//     credentials DIFFERENT describes and checking neither sees the other's.
//  4. The search term is the only caller-supplied value inside SOQL and SOQL has no
//     bind variables, so the escaping is asserted on the query string actually
//     sent, not on the escaper in isolation.
//  5. Retired picklist values, inactive users and the Master record type are all
//     things Salesforce hands back and then rejects on write.
//  6. Every marker names an endpoint the service really serves and carries the auth
//     trio, or the dropdown 404s silently.
//
// Package-level state (the host override, the shared client's Transport, the
// describe cache) is mutated here, so nothing in this file may run in parallel.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// salesforceProxySlugs is every route this file exercises. Kept in one place so a
// new proxy that skips a guard shows up as a missing entry rather than as an
// untested handler.
var salesforceProxySlugs = []string{
	"salesforce-objects",
	"salesforce-fields",
	"salesforce-picklist",
	"salesforce-external-id-fields",
	"salesforce-record-types",
	"salesforce-lookup",
	"salesforce-users",
	"salesforce-owners",
	"salesforce-lead-converted-statuses",
	"salesforce-contract-statuses",
	"salesforce-campaign-member-status",
	"salesforce-list-views",
	"salesforce-reports",
	"salesforce-price-book-entries",
}

// salesforceObjectScopedSlugs are the proxies that cannot answer without an
// object.
var salesforceObjectScopedSlugs = []string{
	"salesforce-fields",
	"salesforce-picklist",
	"salesforce-external-id-fields",
	"salesforce-record-types",
	"salesforce-lookup",
	"salesforce-owners",
	"salesforce-list-views",
}

func setupSalesforceRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/salesforce-objects", svc.getSalesforceObjects)
	r.GET("/api/v1/action/options/salesforce-fields", svc.getSalesforceFields)
	r.GET("/api/v1/action/options/salesforce-picklist", svc.getSalesforcePicklistValues)
	r.GET("/api/v1/action/options/salesforce-external-id-fields", svc.getSalesforceExternalIDFields)
	r.GET("/api/v1/action/options/salesforce-record-types", svc.getSalesforceRecordTypes)
	r.GET("/api/v1/action/options/salesforce-lookup", svc.getSalesforceLookup)
	r.GET("/api/v1/action/options/salesforce-users", svc.getSalesforceUsers)
	r.GET("/api/v1/action/options/salesforce-owners", svc.getSalesforceOwners)
	r.GET("/api/v1/action/options/salesforce-lead-converted-statuses", svc.getSalesforceLeadConvertedStatuses)
	r.GET("/api/v1/action/options/salesforce-contract-statuses", svc.getSalesforceContractStatuses)
	r.GET("/api/v1/action/options/salesforce-campaign-member-status", svc.getSalesforceCampaignMemberStatus)
	r.GET("/api/v1/action/options/salesforce-list-views", svc.getSalesforceListViews)
	r.GET("/api/v1/action/options/salesforce-reports", svc.getSalesforceReports)
	r.GET("/api/v1/action/options/salesforce-price-book-entries", svc.getSalesforcePriceBookEntries)
	return r
}

// getSalesforceOptions calls one picker and returns the decoded body plus the
// status, so a test can assert the "always 200" contract as well as the payload.
// A key absent from params is a query parameter the editor did not send at all,
// which is what "missing input" means here.
func getSalesforceOptions(r *gin.Engine, slug string, params map[string]string) (map[string]interface{}, int) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/action/options/"+slug+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body, rec.Code
}

func sfOptionValues(body map[string]interface{}) []string {
	raw, _ := body["options"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]interface{}); ok {
			out = append(out, m["value"].(string))
		}
	}
	return out
}

func sfOptionNames(body map[string]interface{}) []string {
	raw, _ := body["options"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]interface{}); ok {
			out = append(out, m["name"].(string))
		}
	}
	return out
}

func sfError(body map[string]interface{}) string {
	msg, _ := body["error"].(string)
	return msg
}

// sfClearDescribeCache empties the package-level describe cache. It is keyed on
// the credential AND the base URL, and every stub gets a fresh base URL, but a
// test that reuses a token across stubs would otherwise read a previous test's
// describe.
func sfClearDescribeCache() {
	salesforceCacheMu.Lock()
	salesforceCache = map[string]salesforceCacheEntry{}
	salesforceCacheMu.Unlock()
}

// salesforceStub is a stand-in org behind the host-override seam. It records the
// SOQL of every /query call and the Authorization header of every request, so a
// test can assert what was actually asked of Salesforce — the only way to prove
// the "never page a whole table" and escaping properties.
type salesforceStub struct {
	server  *httptest.Server
	mu      sync.Mutex
	queries []string
	auths   []string
	handler func(w http.ResponseWriter, r *http.Request) bool
}

func (s *salesforceStub) recorded() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...), append([]string(nil), s.auths...)
}

func (s *salesforceStub) soql() []string {
	q, _ := s.recorded()
	return q
}

func (s *salesforceStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries, s.auths = nil, nil
}

func newSalesforceStub(t *testing.T) *salesforceStub {
	t.Helper()
	stub := &salesforceStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.auths = append(stub.auths, r.Header.Get("Authorization"))
		if q := r.URL.Query().Get("q"); q != "" {
			stub.queries = append(stub.queries, q)
		}
		stub.mu.Unlock()
		if stub.handler != nil && stub.handler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))

	prev := salesforceOptionsHostOverride
	salesforceOptionsHostOverride = stub.server.URL
	sfClearDescribeCache()
	t.Cleanup(func() {
		salesforceOptionsHostOverride = prev
		stub.server.Close()
		sfClearDescribeCache()
	})
	return stub
}

// ---------------------------------------------------------------------------
// The interception seam
//
// The host override makes the proxies talk to an httptest server, but it also
// SWITCHES OFF instance-URL validation — so it cannot be used to prove a crafted
// host is refused. For that the override is cleared (validation back on) and the
// shared client's Transport is replaced with a recorder, which proves the
// stronger property: with a crafted instance_url no request is ever handed to the
// transport at all, so the token cannot leave the process even in principle.
// ---------------------------------------------------------------------------

type sfRecordedRequest struct {
	Host string
	URL  string
	Auth string
}

type sfRecordingTransport struct {
	mu       sync.Mutex
	requests []sfRecordedRequest
	respond  func(*http.Request) (*http.Response, error)
}

func (rt *sfRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.requests = append(rt.requests, sfRecordedRequest{
		Host: req.URL.Host,
		URL:  req.URL.String(),
		Auth: req.Header.Get("Authorization"),
	})
	rt.mu.Unlock()
	if rt.respond != nil {
		return rt.respond(req)
	}
	return sfJSONResponse(req, http.StatusOK, `{"records":[]}`), nil
}

func (rt *sfRecordingTransport) seen() []sfRecordedRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]sfRecordedRequest(nil), rt.requests...)
}

func (rt *sfRecordingTransport) reset() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.requests = nil
}

func sfJSONResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// sfIntercept clears the host override (so instance_url validation runs for real)
// and swaps the shared client's Transport for a recorder.
func sfIntercept(t *testing.T) *sfRecordingTransport {
	t.Helper()
	rt := &sfRecordingTransport{}

	prevOverride := salesforceOptionsHostOverride
	prevTransport := salesforceOptionsHTTPClient.Transport
	salesforceOptionsHostOverride = ""
	salesforceOptionsHTTPClient.Transport = rt
	sfClearDescribeCache()

	t.Cleanup(func() {
		salesforceOptionsHostOverride = prevOverride
		salesforceOptionsHTTPClient.Transport = prevTransport
		sfClearDescribeCache()
	})
	return rt
}

const sfToken = "00D5f000000abcd!ARoAQF.token"

// sfAuth is the query the editor forwards for a pasted (non-secret) token.
func sfAuth(extra map[string]string) map[string]string {
	params := map[string]string{
		"access_token": sfToken,
		"instance_url": "https://acme.my.salesforce.com",
	}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// sfRequiredExtras are the sibling inputs a slug needs before it will attempt a
// call at all, so a test about something else is not answered by "choose an
// object first".
func sfRequiredExtras(slug string) map[string]string {
	switch slug {
	case "salesforce-picklist":
		return map[string]string{"object": "Lead", "field": "Status"}
	case "salesforce-fields", "salesforce-external-id-fields", "salesforce-record-types",
		"salesforce-lookup", "salesforce-owners", "salesforce-list-views":
		return map[string]string{"object": "Lead"}
	case "salesforce-campaign-member-status":
		return map[string]string{"campaign_id": "7015f000000abcdAAA"}
	case "salesforce-contract-statuses":
		// OUR parameter, baked into the marker endpoint — the handler refuses an
		// unknown category before it looks at the connection at all.
		return map[string]string{"status_code": "Activated"}
	default:
		return map[string]string{}
	}
}

// sfUnescapedQuotes counts the single quotes in a SOQL statement that are NOT
// preceded by an odd number of backslashes — i.e. the ones that actually open or
// close a literal. A statement with a caller-supplied value in it must have
// exactly two per literal; anything else means the value broke out.
func sfUnescapedQuotes(soql string) int {
	n, backslashes := 0, 0
	for _, ch := range soql {
		switch ch {
		case '\\':
			backslashes++
		case '\'':
			if backslashes%2 == 0 {
				n++
			}
			backslashes = 0
		default:
			backslashes = 0
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 1. Missing inputs are inline messages, never HTTP errors
// ---------------------------------------------------------------------------

// The editor renders {"error": …} in place of the dropdown and lets the operator
// type the value. A 4xx or 5xx is rendered as nothing at all, so the input just
// never loads — the failure mode this contract exists to prevent.
func TestSalesforceMissingInputsAreAlwaysHTTP200(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)
	r := setupSalesforceRouter(&Service{})

	for _, slug := range salesforceProxySlugs {
		// No access token at all.
		rt.reset()
		params := sfRequiredExtras(slug)
		params["instance_url"] = "https://acme.my.salesforce.com"
		body, code := getSalesforceOptions(r, slug, params)
		g.Expect(code).To(Equal(http.StatusOK), "%s: a missing token must not be an HTTP error", slug)
		g.Expect(sfError(body)).To(ContainSubstring("Connect Salesforce"), slug)
		g.Expect(rt.seen()).To(BeEmpty(), "%s: no token, so nothing to ask Salesforce", slug)

		// A token but no instance URL — there is no host to send it to.
		rt.reset()
		params = sfRequiredExtras(slug)
		params["access_token"] = sfToken
		body, code = getSalesforceOptions(r, slug, params)
		g.Expect(code).To(Equal(http.StatusOK), "%s: a missing instance URL must not be an HTTP error", slug)
		g.Expect(sfError(body)).To(ContainSubstring("Instance URL"), slug)
		g.Expect(rt.seen()).To(BeEmpty(), "%s: no host, so no request may be attempted", slug)
		g.Expect(sfError(body)).ToNot(ContainSubstring(sfToken), "%s: the token must never be echoed back", slug)
	}

	// The object-scoped proxies with a perfectly good connection but no object.
	for _, slug := range salesforceObjectScopedSlugs {
		rt.reset()
		body, code := getSalesforceOptions(r, slug, sfAuth(nil))
		g.Expect(code).To(Equal(http.StatusOK), "%s: a missing object must not be an HTTP error", slug)
		g.Expect(sfError(body)).ToNot(BeEmpty(), "%s: a missing object must be explained inline", slug)
		g.Expect(rt.seen()).To(BeEmpty(), "%s: an unset object must not reach the org", slug)
	}

	// The two-hop picker with no campaign, and the picklist picker with an object
	// but no field.
	rt.reset()
	body, code := getSalesforceOptions(r, "salesforce-campaign-member-status", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Campaign"))
	g.Expect(rt.seen()).To(BeEmpty())

	rt.reset()
	body, code = getSalesforceOptions(r, "salesforce-picklist", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).ToNot(BeEmpty())
	g.Expect(rt.seen()).To(BeEmpty())
}

// A value the editor could not resolve (a flow variable) is not a host, an object
// or a field, and must never be sent as one.
func TestSalesforceUnresolvedVariablesNeverReachTheOrg(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)
	r := setupSalesforceRouter(&Service{})

	for _, params := range []map[string]string{
		{"access_token": sfToken, "instance_url": "${node.abc.output.url}"},
		{"access_token": sfToken, "instance_url": "https://acme.my.salesforce.com", "object": "${node.abc.output.obj}"},
	} {
		rt.reset()
		body, code := getSalesforceOptions(r, "salesforce-fields", params)
		g.Expect(code).To(Equal(http.StatusOK))
		g.Expect(sfError(body)).To(ContainSubstring("type the value in"))
		g.Expect(rt.seen()).To(BeEmpty())
	}
}

// ---------------------------------------------------------------------------
// 2. A crafted instance_url is refused BEFORE the token is attached
// ---------------------------------------------------------------------------

// The strongest statement available: for each crafted host the recording
// transport — which sits below every guard in the client — is never handed a
// request. The token therefore cannot leave the process, whatever DNS says.
func TestSalesforceCraftedInstanceURLNeverReachesTheNetwork(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)
	r := setupSalesforceRouter(&Service{})

	crafted := []struct{ raw, why string }{
		{"https://evil.example", "an unrelated host"},
		{"https://evilsalesforce.com", "suffix without the dot — 'evilsalesforce.com' ends in 'salesforce.com'"},
		{"https://salesforce.com.evil.example", "the salesforce name in the middle"},
		{"https://acme.my.salesforce.com.evil.example", "a real org name in the middle"},
		{"https://169.254.169.254", "the cloud metadata service"},
		{"http://localhost", "the api's own box"},
		{"http://127.0.0.1:8888", "the api's own port"},
		{"https://salesforce.com", "the bare suffix, no org subdomain"},
		{"https://force.com", "the bare suffix, no org subdomain"},
		{"https://x.my.salesforce.com@evil.example", "userinfo smuggling — the host is evil.example"},
		{"https://x.my.salesforce.com:8443@evil.example", "userinfo smuggling with a port"},
		{"//evil.example", "scheme-relative"},
		{"https://internal.svc.cluster.local", "an in-cluster name"},
	}

	for _, tc := range crafted {
		rt.reset()
		body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
			"access_token": sfToken,
			"instance_url": tc.raw,
		})

		g.Expect(code).To(Equal(http.StatusOK), "%s (%s)", tc.raw, tc.why)
		g.Expect(sfError(body)).ToNot(BeEmpty(), "%s (%s) must be refused", tc.raw, tc.why)
		g.Expect(sfError(body)).ToNot(ContainSubstring(sfToken), "%s: the token must not be echoed", tc.raw)
		g.Expect(rt.seen()).To(BeEmpty(),
			"%s (%s): a request was made — the token was attached before the host was validated", tc.raw, tc.why)
	}

	// Every proxy shares the guard, not just the one above.
	for _, slug := range salesforceProxySlugs {
		rt.reset()
		params := sfRequiredExtras(slug)
		params["access_token"] = sfToken
		params["instance_url"] = "https://evil.example"
		_, code := getSalesforceOptions(r, slug, params)
		g.Expect(code).To(Equal(http.StatusOK), slug)
		g.Expect(rt.seen()).To(BeEmpty(), "%s: crafted instance_url reached the network", slug)
	}
}

// The positive control for the test above: without it, "no request was made"
// would also pass if the proxies never made requests at all.
func TestSalesforceLegitimateInstanceURLIsReachedWithTheToken(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)
	r := setupSalesforceRouter(&Service{})

	for _, raw := range []string{
		"https://acme.my.salesforce.com",
		"acme.my.salesforce.com",
		"http://acme.my.salesforce.com",
		"https://ACME.MY.SALESFORCE.COM",
		"https://acme.lightning.force.com/lightning/o/Lead/list",
		"https://agency.my.salesforce.mil",
		"https://na1.cloudforce.com",
		"https://user:pw@acme.my.salesforce.com:8443/services/data",
	} {
		rt.reset()
		body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
			"access_token": sfToken,
			"instance_url": raw,
		})
		g.Expect(code).To(Equal(http.StatusOK), raw)
		g.Expect(body).ToNot(HaveKey("error"), raw)

		seen := rt.seen()
		g.Expect(seen).To(HaveLen(1), raw)
		g.Expect(seen[0].Auth).To(Equal("Bearer "+sfToken), raw)
		// Whatever was pasted, the request goes to the bare https origin: a port,
		// path or query in the instance URL must not displace the API path.
		g.Expect(seen[0].URL).To(HavePrefix("https://"+strings.ToLower(hostOf(raw))+"/services/data/v"+salesforceAPIVersion+"/"), raw)
	}
}

// hostOf is the expected host for the positive-control table above, derived
// independently of normaliseSalesforceInstanceURL so the assertion is not the
// implementation restating itself.
func hostOf(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

func TestSalesforceNormaliseStripsUserinfoPortPathAndQuery(t *testing.T) {
	g := NewWithT(t)

	// A crafted base must not be able to displace the API path appended to it.
	g.Expect(normaliseSalesforceInstanceURL("https://user:pw@acme.my.salesforce.com:8443/x?y=1")).
		To(Equal("https://acme.my.salesforce.com"))
	// http is upgraded rather than honoured — a silent downgrade would put the
	// token on the wire in clear.
	g.Expect(normaliseSalesforceInstanceURL("http://acme.my.salesforce.com")).
		To(Equal("https://acme.my.salesforce.com"))
	// Userinfo that LOOKS like an org name is discarded, not adopted.
	g.Expect(normaliseSalesforceInstanceURL("https://acme.my.salesforce.com@evil.example")).
		To(Equal("https://evil.example"))
	// An IPv6 literal keeps its brackets when the port is dropped.
	g.Expect(normaliseSalesforceInstanceURL("https://[fd00::1]:443/x")).To(Equal("https://[fd00::1]"))
}

func TestSalesforceInstanceURLValidationTable(t *testing.T) {
	g := NewWithT(t)

	for _, raw := range []string{
		"https://acme.my.salesforce.com",
		"acme.my.salesforce.com",
		"https://acme.lightning.force.com/lightning/o/Lead/list",
		"https://agency.my.salesforce.mil",
		"https://na1.cloudforce.com",
		"https://ACME.MY.SALESFORCE.COM",
	} {
		g.Expect(validateSalesforceInstanceURL(normaliseSalesforceInstanceURL(raw))).
			To(BeEmpty(), "expected %q to be accepted", raw)
	}

	for _, raw := range []string{
		"https://salesforce.com", "https://force.com", "https://salesforce.mil", "https://cloudforce.com",
		"https://evilsalesforce.com", "https://myforce.com",
		"https://acme.my.salesforce.com.evil.example",
		"https://salesforce.com.evil.example",
		"https://x.my.salesforce.com@evil.example",
		"https://attacker.example", "http://169.254.169.254", "http://localhost",
		"", "   ", "://",
	} {
		g.Expect(validateSalesforceInstanceURL(normaliseSalesforceInstanceURL(raw))).
			ToNot(BeEmpty(), "expected %q to be refused", raw)
	}
}

// ---------------------------------------------------------------------------
// 3. SOQL escaping — the injection boundary
// ---------------------------------------------------------------------------

// The search term is the only caller-supplied VALUE that reaches SOQL, and SOQL
// has no bind variables over REST. This asserts the statement actually sent, not
// the escaper in isolation: an escaper that is correct but bypassed is the bug
// this catches.
func TestSalesforceLookupSearchTermCannotEscapeTheLikeLiteral(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	// Every dangerous character at once: a quote to close the literal, a backslash
	// to neutralise the escape of the quote, and the two LIKE wildcards.
	const search = `x' OR Id != null-- 50%_\`
	_, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Account",
		"search": search,
	}))
	g.Expect(code).To(Equal(http.StatusOK))

	soql := stub.soql()
	g.Expect(soql).To(HaveLen(1))
	g.Expect(soql[0]).To(Equal(
		`SELECT Id, Name FROM Account WHERE Name LIKE '%x\' OR Id != null-- 50\%\_\\%' ORDER BY Name LIMIT 100`))

	// Structural, not textual: exactly the two quotes that delimit the literal are
	// unescaped, so nothing the operator typed opened or closed a string.
	g.Expect(sfUnescapedQuotes(soql[0])).To(Equal(2),
		"the search term broke out of the LIKE literal: %s", soql[0])

	// A trailing lone backslash must not swallow the escape of the closing quote.
	g.Expect(strings.HasSuffix(soql[0], `\\%' ORDER BY Name LIMIT 100`)).To(BeTrue(), soql[0])
}

func TestSalesforceLikeEscaperKeepsWildcardsLiteral(t *testing.T) {
	g := NewWithT(t)

	// Backslash is replaced first and strings.Replacer makes ONE left-to-right
	// pass, so the backslashes it introduces are never re-processed.
	g.Expect(salesforceLikeEscaper.Replace(`50%`)).To(Equal(`50\%`))
	g.Expect(salesforceLikeEscaper.Replace(`a_b`)).To(Equal(`a\_b`))
	g.Expect(salesforceLikeEscaper.Replace(`O'Brien`)).To(Equal(`O\'Brien`))
	g.Expect(salesforceLikeEscaper.Replace(`back\slash`)).To(Equal(`back\\slash`))
	// The double-escaping trap: an already-escaped quote must not become \\' —
	// which would end the literal.
	g.Expect(salesforceLikeEscaper.Replace(`\'`)).To(Equal(`\\\'`))
	g.Expect(sfUnescapedQuotes(`'` + salesforceLikeEscaper.Replace(`\'`) + `'`)).To(Equal(2))
	g.Expect(salesforceLikeEscaper.Replace("line\nbreak")).To(Equal(`line\nbreak`))
}

// Identifiers cannot be quoted in SOQL, so a whitelist — not escaping — is the
// only defence available for them.
func TestSalesforceIdentifiersAreWhitelisted(t *testing.T) {
	g := NewWithT(t)

	for _, ok := range []string{"Account", "Invoice__c", "NS__Thing__c", "Case", "Pricebook2"} {
		obj, msg := validateSalesforceObject(ok)
		g.Expect(msg).To(BeEmpty(), ok)
		g.Expect(obj).To(Equal(ok))
	}
	for _, bad := range []string{
		"Account WHERE Id != null", "Account'", "1Account", "Acc-ount", "Account;",
		"Account)", "Account,User", "Account%", "Account\nUser", "_Account", "",
	} {
		_, msg := validateSalesforceObject(bad)
		g.Expect(msg).ToNot(BeEmpty(), "%q must be refused as an object name", bad)
	}

	f, msg := validateSalesforceField("Account.Name")
	g.Expect(msg).To(BeEmpty())
	g.Expect(f).To(Equal("Account.Name"))
	for _, bad := range []string{"Name FROM User--", "Name'", "Name)", "Name Status", "1Name"} {
		_, msg := validateSalesforceField(bad)
		g.Expect(msg).ToNot(BeEmpty(), "%q must be refused as a field name", bad)
	}
}

// A crafted object inside the comma-separated polymorphic list must stop the
// whole picker, not be quietly dropped while its neighbours are queried.
func TestSalesforceLookupRejectsACraftedObjectInTheList(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Contact,Lead WHERE Id != null",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).ToNot(BeEmpty())
	g.Expect(stub.soql()).To(BeEmpty(), "nothing may be queried once one object is invalid")
}

// ---------------------------------------------------------------------------
// 4. Lookup — the "never page a whole table" property
// ---------------------------------------------------------------------------

func TestSalesforceLookupIssuesOneFilteredLimitedQuery(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[
				{"name":"Id","label":"Record ID","type":"id"},
				{"name":"Name","label":"Account Name","type":"string","nameField":true,"filterable":true,"sortable":true}
			]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[
			{"Id":"0015f00000AbCdEAAV","Name":"Acme Manufacturing"},
			{"Id":"0015f00000AbCdFAAV","Name":"Zeta Ltd"}
		]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Account",
		"search": "acme",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfOptionValues(body)).To(ConsistOf("0015f00000AbCdEAAV", "0015f00000AbCdFAAV"))

	soql := stub.soql()
	g.Expect(soql).To(HaveLen(1), "exactly one SOQL call, never a paged sweep")
	g.Expect(soql[0]).To(Equal(
		"SELECT Id, Name FROM Account WHERE Name LIKE '%acme%' ORDER BY Name LIMIT 100"))
}

// A polymorphic picker is capped: a crafted endpoint must not turn one dropdown
// into an unbounded fan-out of org queries.
func TestSalesforceLookupCapsThePolymorphicFanOut(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	_, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Account,Contact,Lead,Opportunity,Case,Campaign,Task,Event",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()).To(HaveLen(salesforceMaxLookupObjects))
}

func TestSalesforceLookupUsesTheObjectsOwnNameField(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			// Case has no Name — it is CaseNumber, with Subject alongside. A
			// hard-coded "SELECT Id, Name" fails outright here.
			_, _ = w.Write([]byte(`{"fields":[
				{"name":"CaseNumber","label":"Case Number","type":"string","nameField":true,"filterable":true,"sortable":true},
				{"name":"Subject","label":"Subject","type":"string"}
			]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"5005f00000AbCdEAAV","CaseNumber":"00001234","Subject":"Printer jammed"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, _ := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{"object": "Case"}))

	g.Expect(stub.soql()[0]).To(ContainSubstring("SELECT Id, CaseNumber, Subject FROM Case"))
	// The case number alone identifies nothing, so the subject rides along.
	g.Expect(sfOptionNames(body)).To(ConsistOf("00001234 — Printer jammed"))
}

// A name field that is neither filterable nor sortable (a formula) must degrade
// to a bounded list rather than producing a statement Salesforce rejects.
func TestSalesforceLookupDegradesOnAnUnfilterableNameField(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":false,"sortable":false}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"a015f00000AbCdEAAV","Name":"Widget"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Widget__c", "search": "wid",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfOptionValues(body)).To(ConsistOf("a015f00000AbCdEAAV"))

	soql := stub.soql()[0]
	g.Expect(soql).ToNot(ContainSubstring("WHERE"))
	g.Expect(soql).ToNot(ContainSubstring("ORDER BY"))
	g.Expect(soql).To(ContainSubstring("LIMIT 100"), "still bounded — a formula name is not a licence to sweep")
}

func TestSalesforceLookupMergesPolymorphicObjects(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		if strings.Contains(r.URL.Query().Get("q"), "FROM Contact") {
			_, _ = w.Write([]byte(`{"records":[{"Id":"0035f00000AaaaaAAA","Name":"Jane Smith"}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"00Q5f00000BbbbbAAA","Name":"Bob Jones"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, _ := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{"object": "Contact,Lead"}))

	// WhoId takes a Contact OR a Lead, and the label has to say which.
	g.Expect(sfOptionNames(body)).To(ConsistOf("Contact: Jane Smith", "Lead: Bob Jones"))
	g.Expect(stub.soql()).To(HaveLen(2))
}

// email_send's related_record_id names five objects, and each one costs a
// describe followed by a query. Walked in sequence that is ten round trips one
// after another — 2.7 s cold against a bare Developer Edition org, worse on a
// real one, and the whole time the dropdown has nothing in it.
//
// The barrier is the assertion: each describe waits for all five to have arrived
// before answering. A sequential handler never gets past the first one and the
// stub falls through on its timeout, leaving a peak concurrency of 1.
func TestSalesforceLookupSearchesPolymorphicObjectsConcurrently(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)

	const objects = 5
	var mu sync.Mutex
	var inFlight, peak int
	allArrived := make(chan struct{})
	var arrivals int

	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"records":[]}`))
			return true
		}

		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		arrivals++
		if arrivals == objects {
			close(allArrived)
		}
		mu.Unlock()

		select {
		case <-allArrived:
		case <-time.After(3 * time.Second): // sequential: nobody else is coming
		}

		mu.Lock()
		inFlight--
		mu.Unlock()

		_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	_, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Account,Contact,Lead,Opportunity,Case",
	}))
	g.Expect(code).To(Equal(http.StatusOK))

	mu.Lock()
	got := peak
	mu.Unlock()
	g.Expect(got).To(Equal(objects),
		"the %d objects of a polymorphic picker were searched %d at a time", objects, got)
}

// One object of a polymorphic set failing (a Lead the user cannot see) must not
// blank a picker that has perfectly good Contacts in it.
func TestSalesforceLookupSurvivesOneObjectFailing(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/Lead/") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`[{"message":"no access","errorCode":"INSUFFICIENT_ACCESS"}]`))
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"0035f00000AaaaaAAA","Name":"Jane Smith"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{"object": "Contact,Lead"}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(sfOptionNames(body)).To(ConsistOf("Contact: Jane Smith"))
}

// orgWideEmailAddressDescribe is the real describe of OrgWideEmailAddress, taken
// from a live org. It is STANDARD schema — no org has it any other way — and it
// flags no nameField and carries none of the generic Name / Subject / Title /
// CaseNumber / DeveloperName fallbacks, so nothing an administrator does could
// make the generic path work.
const orgWideEmailAddressDescribe = `{"fields":[
	{"name":"Id","label":"Id","type":"id","filterable":true,"sortable":true},
	{"name":"IsVerified","label":"Verified","type":"boolean","filterable":true,"sortable":true},
	{"name":"Address","label":"Email Address","type":"email","filterable":true,"sortable":true},
	{"name":"DisplayName","label":"Display Name","type":"string","filterable":true,"sortable":true},
	{"name":"Purpose","label":"Purpose","type":"picklist","filterable":true,"sortable":true}
]}`

// email_send's "Send As: an Org-Wide Email Address" input is fed by
// /salesforce-lookup?object=OrgWideEmailAddress. Without a label-field override
// that picker can never return a single option in any org — it walks the name
// fallbacks, finds none, and tells the operator a field is missing on an input
// where nothing they can do would ever populate the list.
func TestSalesforceLookupHandlesObjectsWithNoNameField(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(orgWideEmailAddressDescribe))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[
			{"Id":"0D25f000000AbcdCAC","DisplayName":"Support","Address":"support@acme.com"}
		]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "OrgWideEmailAddress",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())
	// "Support" alone is not enough to choose between two org-wide addresses, so
	// the address rides along as the second column.
	g.Expect(sfOptionNames(body)).To(Equal([]string{"Support — support@acme.com"}))
	g.Expect(sfOptionValues(body)).To(Equal([]string{"0D25f000000AbcdCAC"}))
	g.Expect(stub.soql()[0]).To(Equal(
		"SELECT Id, DisplayName, Address FROM OrgWideEmailAddress ORDER BY DisplayName LIMIT 100"))
}

// An object with genuinely nothing to show gets a sentence about THAT, not
// "Salesforce doesn't have that field on this object" — which sends the operator
// looking for a mistake they did not make.
func TestSalesforceLookupWithNoLabelFieldSaysSo(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Id","label":"Id","type":"id","filterable":true,"sortable":true}]}`))
			return true
		}
		return false
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Opaque__c",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(Equal(
		"Salesforce doesn't publish a name for Opaque__c records, so they can't be listed here — type the record ID in"))
	g.Expect(sfError(body)).ToNot(ContainSubstring("doesn't have that field"))
	// And it never asked the org for rows it could not label.
	g.Expect(stub.soql()).To(BeEmpty())
}

// ---------------------------------------------------------------------------
// 5. Picklists, fields, record types, external ids
// ---------------------------------------------------------------------------

const salesforceDescribeStub = `{
	"fields":[
		{"name":"Id","label":"Record ID","type":"id","idLookup":true,"filterable":true,"sortable":true},
		{"name":"Status","label":"Lead Status","type":"picklist","createable":true,"updateable":true,
		 "filterable":true,"sortable":true,
		 "picklistValues":[
			{"active":true,"label":"Working - Contacted","value":"Working - Contacted"},
			{"active":false,"label":"Retired Status","value":"Retired Status"},
			{"active":true,"label":"Open - Not Contacted","value":"Open - Not Contacted"}
		 ]},
		{"name":"Email","label":"Email","type":"email","createable":true,"updateable":true,"externalId":false,"idLookup":true,
		 "filterable":true,"sortable":true},
		{"name":"External_Ref__c","label":"External Ref","type":"string","createable":true,"externalId":true,
		 "filterable":true,"sortable":true},
		{"name":"CreatedDate","label":"Created Date","type":"datetime","createable":false,"updateable":false,
		 "filterable":true,"sortable":true},
		{"name":"Description","label":"Description","type":"textarea","createable":true,"updateable":true,
		 "filterable":false,"sortable":false},
		{"name":"BillingAddress","label":"Billing Address","type":"address","filterable":true,"sortable":false}
	],
	"recordTypeInfos":[
		{"recordTypeId":"012000000000000AAA","name":"Master","active":true,"available":true,"master":true},
		{"recordTypeId":"0125f000000abcdAAA","name":"Partner Lead","active":true,"available":true,"master":false},
		{"recordTypeId":"0125f000000efghAAA","name":"Retired Lead","active":false,"available":false,"master":false}
	]
}`

func salesforceDescribeOnlyStub(t *testing.T) *salesforceStub {
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/describe") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(salesforceDescribeStub))
			return true
		}
		return false
	}
	return stub
}

// A value retired in Setup is still in the describe, and offering it produces a
// write Salesforce rejects — the defect the active filter exists to avoid.
func TestSalesforcePicklistDropsRetiredValues(t *testing.T) {
	g := NewWithT(t)
	salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-picklist", sfAuth(map[string]string{
		"object": "Lead", "field": "Status",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfOptionValues(body)).ToNot(ContainElement("Retired Status"))
	g.Expect(sfOptionNames(body)).ToNot(ContainElement("Retired Status"))

	// The remaining values keep Salesforce's own order — for stages and statuses
	// that order IS the process, so alphabetising it is wrong. The stub lists
	// "Working" before "Open" precisely so a sort would show up here.
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Working - Contacted", "Open - Not Contacted"}))
}

func TestSalesforcePicklistOnANonPicklistFieldExplainsItself(t *testing.T) {
	g := NewWithT(t)
	salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-picklist", sfAuth(map[string]string{
		"object": "Lead", "field": "Email",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("type the value in"))

	// A field the org does not have at all is a different sentence, but still a
	// sentence.
	body, code = getSalesforceOptions(r, "salesforce-picklist", sfAuth(map[string]string{
		"object": "Lead", "field": "Nonexistent__c",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Nonexistent__c"))
}

func TestSalesforceFieldsRespectTheFilter(t *testing.T) {
	g := NewWithT(t)
	stub := salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	all, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead", "filter": "all"}))
	g.Expect(sfOptionValues(all)).To(ContainElement("CreatedDate"))

	createable, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead", "filter": "createable"}))
	g.Expect(sfOptionValues(createable)).ToNot(ContainElement("CreatedDate"))
	g.Expect(sfOptionValues(createable)).To(ContainElement("Status"))

	updateable, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead", "filter": "updateable"}))
	g.Expect(sfOptionValues(updateable)).ToNot(ContainElement("External_Ref__c"))

	picklist, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead", "filter": "picklist"}))
	g.Expect(sfOptionValues(picklist)).To(Equal([]string{"Status"}))

	// No filter at all behaves as "all" rather than as an error.
	none, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(sfOptionValues(none)).To(ConsistOf(sfOptionValues(all)))

	// The filter is OURS, baked into the marker's endpoint — an unknown one is a
	// bug, and it must be refused before it can reach the org.
	stub.reset()
	sfClearDescribeCache()
	bad, code := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead", "filter": "sneaky"}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(bad)).ToNot(BeEmpty())
	q, auths := stub.recorded()
	g.Expect(q).To(BeEmpty())
	g.Expect(auths).To(BeEmpty(), "an unknown filter must not cause a describe")
}

// Salesforce refuses to sort or filter on some of the fields its own describe
// returns, and says so only at run time:
//
//	ORDER BY Description  -> MALFORMED_QUERY "field 'Description' can not be sorted in a query call"
//	WHERE Description      -> INVALID_FIELD  "field 'Description' can not be filtered in a query call"
//	WHERE BillingAddress   -> INVALID_FIELD  "Address fields can only be filtered using Distance expressions"
//
// (all three reproduced live). Offering them in Sort By / Filter Field / Look Up
// By is a dropdown row that is always an error, with nothing to tell an operator
// which of the seventy rows are the poisoned ones — the retired-picklist defect
// one endpoint along.
func TestSalesforceFieldsNeverOfferAnUnsortableOrUnfilterableField(t *testing.T) {
	g := NewWithT(t)
	salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	sortable, code := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{
		"object": "Lead", "filter": "sortable",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(sortable)).To(BeEmpty())
	g.Expect(sfOptionValues(sortable)).To(ContainElement("CreatedDate"))
	g.Expect(sfOptionValues(sortable)).ToNot(ContainElement("Description"))
	g.Expect(sfOptionValues(sortable)).ToNot(ContainElement("BillingAddress"))

	filterable, code := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{
		"object": "Lead", "filter": "filterable",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(filterable)).To(BeEmpty())
	g.Expect(sfOptionValues(filterable)).To(ContainElement("Email"))
	g.Expect(sfOptionValues(filterable)).ToNot(ContainElement("Description"))
	// BillingAddress reports filterable:true and Salesforce still rejects it
	// outside a DISTANCE expression, so the flag alone is not enough.
	g.Expect(sfOptionValues(filterable)).ToNot(ContainElement("BillingAddress"))

	// The SELECT list is a different question: Description is perfectly
	// selectable, so the `fields` pickers must keep offering it.
	all, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{
		"object": "Lead", "filter": "all",
	}))
	g.Expect(sfOptionValues(all)).To(ContainElement("Description"))
	g.Expect(sfOptionValues(all)).To(ContainElement("BillingAddress"))
}

func TestSalesforceExternalIDFieldsAndRecordTypes(t *testing.T) {
	g := NewWithT(t)
	salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	ext, _ := getSalesforceOptions(r, "salesforce-external-id-fields", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(sfOptionValues(ext)).To(ConsistOf("Id", "Email", "External_Ref__c"))
	// A field that is neither an External Id nor id-lookup cannot key an upsert.
	g.Expect(sfOptionValues(ext)).ToNot(ContainElement("CreatedDate"))

	rt, _ := getSalesforceOptions(r, "salesforce-record-types", sfAuth(map[string]string{"object": "Lead"}))
	// Inactive is dropped, and so is Master — naming Master is never what an
	// operator means, and it is present in every describe.
	g.Expect(sfOptionValues(rt)).To(Equal([]string{"0125f000000abcdAAA"}))
	g.Expect(sfOptionNames(rt)).ToNot(ContainElement("Master"))
}

// ---------------------------------------------------------------------------
// 6. Users and owners
// ---------------------------------------------------------------------------

func TestSalesforceUsersExcludeInactiveByDefault(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		// A real org answers the filter; the stub returns only the active user so
		// the assertion below is about the STATEMENT, which is what does the work.
		_, _ = w.Write([]byte(`{"records":[{"Id":"0055f000004XyzAAAS","Name":"Jane Smith","Username":"jane@acme.com"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))

	g.Expect(code).To(Equal(http.StatusOK))
	// Offering a deactivated user produces INVALID_CROSS_REFERENCE_KEY on write,
	// which reads to an operator like a broken integration.
	g.Expect(stub.soql()[0]).To(Equal(
		"SELECT Id, Name, Username FROM User WHERE IsActive = true ORDER BY Name LIMIT 200"))
	// Two Jane Smiths in one org is ordinary; the username disambiguates them.
	g.Expect(sfOptionNames(body)).To(Equal([]string{"Jane Smith (jane@acme.com)"}))

	stub.reset()
	_, _ = getSalesforceOptions(r, "salesforce-users", sfAuth(map[string]string{"include_inactive": "true"}))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("IsActive"))
	g.Expect(stub.soql()[0]).To(ContainSubstring("LIMIT 200"), "still bounded")
}

// Every org ships a Chatter Free user (the stock "Chatter Expert"), and
// Salesforce refuses one as an OwnerId: the insert comes back 400
// OP_WITH_INVALID_USER_TYPE_EXCEPTION "Operation not valid for this user type" —
// reproduced live on Lead, Task and Event. So every customer's owner picker
// carries at least one row that is guaranteed to fail, sitting alphabetically
// among the real people.
func TestSalesforceOwnerPickersExcludeUsersWhoCannotOwnRecords(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[{"Id":"0055f000004XyzAAAS","Name":"Jane Smith","Username":"jane@acme.com"}]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	// The merged users-and-queues picker.
	_, code := getSalesforceOptions(r, "salesforce-owners", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(Equal(
		"SELECT Id, Name, Username FROM User WHERE IsActive = true AND UserType NOT IN ('CsnOnly','CsnExternal') ORDER BY Name LIMIT 200"))

	// …and the plain user picker when the marker says the value becomes an owner.
	stub.reset()
	_, _ = getSalesforceOptions(r, "salesforce-users", sfAuth(map[string]string{"owner": "true"}))
	g.Expect(stub.soql()[0]).To(ContainSubstring("UserType NOT IN ('CsnOnly','CsnExternal')"))

	// An input that merely NAMES a user is a different question: user_get,
	// user_update and user_deactivate legitimately target a Chatter Free user, so
	// narrowing that list would be the same defect the other way round.
	stub.reset()
	_, _ = getSalesforceOptions(r, "salesforce-users", sfAuth(nil))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("UserType"))

	// It is an EXCLUDE list, never a UserType = 'Standard' whitelist: Salesforce
	// accepts AutomatedProcess, CloudIntegrationUser and the portal types as
	// owners (each probed live), and a partner-community user owning a registered
	// deal is ordinary in a PRM org.
	g.Expect(stub.soql()).ToNot(ContainElement(ContainSubstring("UserType = 'Standard'")))
	stub.reset()
	_, _ = getSalesforceOptions(r, "salesforce-owners", sfAuth(map[string]string{"object": "Case"}))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("UserType ="))
}

// Every owner_id marker has to reach an owner-filtered list, or the picker it
// feeds offers a user Salesforce will refuse.
func TestSalesforceOwnerMarkersUseAnOwnerFilteredList(t *testing.T) {
	g := NewWithT(t)

	seen := 0
	for key, marker := range salesforceMarkers() {
		if !strings.HasSuffix(key, "#owner_id") {
			continue
		}
		seen++
		slug, values := salesforceMarkerQuery(t, marker.Endpoint)
		switch slug {
		case "salesforce-owners":
			// Always owner-filtered — that is the whole endpoint.
		case "salesforce-users":
			g.Expect(values.Get("owner")).To(Equal("true"),
				"%s fills an OwnerId from the unfiltered user list", key)
		default:
			t.Errorf("%s fills an OwnerId from %q", key, slug)
		}
	}
	g.Expect(seen).To(BeNumerically(">", 0), "no owner_id markers found — the check has drifted")
}

// OwnerId legitimately takes a queue id on Lead and Case, so the queues have to
// be there; and BOTH groups are prefixed unconditionally so the same field never
// reads differently in two orgs.
func TestSalesforceOwnersMergeQueuesAndUsersBothPrefixed(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("q"), "QueueSobject") {
			_, _ = w.Write([]byte(`{"records":[
				{"QueueId":"00G5f000004AbcdEAC","Queue":{"Name":"Tier 1 Support"}},
				{"QueueId":"00G5f000004ZzzzEAC","Queue":{"Name":"Escalations"}}
			]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"0055f000004XyzAAAS","Name":"Jane Smith","Username":"jane@acme.com"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-owners", sfAuth(map[string]string{"object": "Case"}))

	g.Expect(code).To(Equal(http.StatusOK))
	names := sfOptionNames(body)
	g.Expect(names).To(Equal([]string{
		"Queue: Escalations",
		"Queue: Tier 1 Support",
		"User: Jane Smith (jane@acme.com)",
	}))
	g.Expect(sfOptionValues(body)).To(ContainElement("00G5f000004AbcdEAC"))
	g.Expect(sfOptionValues(body)).To(ContainElement("0055f000004XyzAAAS"))

	soql := stub.soql()
	g.Expect(soql).To(HaveLen(2))
	g.Expect(soql[0]).To(ContainSubstring("WHERE IsActive = true"))
	g.Expect(soql[1]).To(Equal(
		"SELECT QueueId, Queue.Name FROM QueueSobject WHERE SobjectType = 'Case' ORDER BY Queue.Name LIMIT 200"))
	g.Expect(sfUnescapedQuotes(soql[1])).To(Equal(2))
}

// The prefix must NOT be conditional on a queue existing: an org with no queues
// on Contact still labels its users "User: …", so the field reads the same
// everywhere.
func TestSalesforceOwnersPrefixUsersEvenWithNoQueues(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("q"), "QueueSobject") {
			_, _ = w.Write([]byte(`{"records":[]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"0055f000004XyzAAAS","Name":"Jane Smith","Username":"jane@acme.com"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, _ := getSalesforceOptions(r, "salesforce-owners", sfAuth(map[string]string{"object": "Contact"}))
	g.Expect(sfOptionNames(body)).To(Equal([]string{"User: Jane Smith (jane@acme.com)"}))
}

// QueueSobject needs a permission a read-only integration user may not have.
// Losing the queues is far better than losing the whole picker.
func TestSalesforceOwnersSurviveAQueueQueryFailure(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("q"), "QueueSobject") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`[{"message":"no access","errorCode":"INSUFFICIENT_ACCESS"}]`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"0055f000004XyzAAAS","Name":"Jane Smith","Username":"jane@acme.com"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-owners", sfAuth(map[string]string{"object": "Lead"}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(sfOptionNames(body)).To(Equal([]string{"User: Jane Smith (jane@acme.com)"}))
}

// ---------------------------------------------------------------------------
// 7. Two-hop, list views, reports, objects
// ---------------------------------------------------------------------------

// Convert Lead's Converted Status is the one input where the Lead.Status picklist
// is the WRONG list. Only the statuses an administrator has ticked "Converted"
// convert; convertLead answers any of the others with INVALID_STATUS "invalid
// convertedStatus" — reproduced live for all three non-converted values of a
// default org, including "Closed - Not Converted", which is exactly the row an
// operator recording a dead lead reaches for.
func TestSalesforceLeadConvertedStatusesOnlyListStatusesThatConvert(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		// A real org answers the WHERE; the stub returns the converted rows so the
		// assertion below is about the STATEMENT, which is what does the work.
		_, _ = w.Write([]byte(`{"records":[
			{"MasterLabel":"Closed - Converted","IsDefault":true},
			{"MasterLabel":"Qualified - Converted","IsDefault":false}
		]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-lead-converted-statuses", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(Equal(
		"SELECT MasterLabel, IsDefault FROM LeadStatus WHERE IsConverted = true ORDER BY SortOrder LIMIT 200"))
	// convertLead takes the LABEL, so the label is both what is shown and what is
	// sent, and the order is the org's own SortOrder rather than alphabetical.
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Closed - Converted", "Qualified - Converted"}))
	g.Expect(sfOptionNames(body)[0]).To(Equal("Closed - Converted (default)"))

	// The marker has to point here and not at the picklist, or the endpoint is
	// dead code and the dropdown still offers three statuses that cannot work.
	marker, registered := dynamicOptionsMetadata["crm/salesforce/lead_convert#converted_status"]
	g.Expect(registered).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/salesforce-lead-converted-statuses"))
}

// An org where no status is marked Converted gets told what to change, not an
// empty box.
func TestSalesforceLeadConvertedStatusesExplainAnEmptyList(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
		return true
	}
	_ = stub

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lead-converted-statuses", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("marked as Converted"))
}

func TestSalesforceCampaignMemberStatusValidatesTheCampaignID(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[
			{"Label":"Sent","SortOrder":1,"IsDefault":true},
			{"Label":"Responded","SortOrder":2,"IsDefault":false}
		]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	// Anything that is not a record id never reaches the SOQL.
	for _, bad := range []string{
		"7015f000000abcd' OR Id != null--",
		"7015f000000abcd'",
		"short",
		"7015f000000abcdAAAAAAAA",
		"7015f000000abc-A",
	} {
		stub.reset()
		body, code := getSalesforceOptions(r, "salesforce-campaign-member-status", sfAuth(map[string]string{
			"campaign_id": bad,
		}))
		g.Expect(code).To(Equal(http.StatusOK), bad)
		g.Expect(sfError(body)).ToNot(BeEmpty(), bad)
		g.Expect(stub.soql()).To(BeEmpty(), "%q reached the org", bad)
	}

	stub.reset()
	body, _ := getSalesforceOptions(r, "salesforce-campaign-member-status", sfAuth(map[string]string{
		"campaign_id": "7015f000000abcdAAA",
	}))
	// CampaignMember.Status holds the status LABEL, not an id, so the label is
	// both what the operator sees and what the flow sends. The order is the
	// campaign's own SortOrder, not alphabetical.
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Sent", "Responded"}))
	g.Expect(sfOptionNames(body)[0]).To(Equal("Sent (default)"))
	g.Expect(stub.soql()[0]).To(ContainSubstring("WHERE CampaignId = '7015f000000abcdAAA'"))
	g.Expect(stub.soql()[0]).To(ContainSubstring("LIMIT 200"))
}

func TestSalesforceReportsServeReportsAndFolders(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[
			{"Id":"00O5f000001AbcdEAC","Name":"Pipeline","FolderName":"Sales Reports"},
			{"Id":"00O5f000001EfghEAC","Name":"Won This Quarter","FolderName":"Sales Reports"},
			{"Id":"00O5f000001IjklEAC","Name":"Pipeline","FolderName":"Exec"}
		]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	// Report names repeat across folders far more often than they are unique, so
	// the folder is part of the label.
	reports, _ := getSalesforceOptions(r, "salesforce-reports", sfAuth(nil))
	g.Expect(sfOptionNames(reports)).To(ConsistOf(
		"Sales Reports / Pipeline", "Sales Reports / Won This Quarter", "Exec / Pipeline"))
	g.Expect(stub.soql()[0]).To(ContainSubstring("LIMIT 200"))

	folders, _ := getSalesforceOptions(r, "salesforce-reports", sfAuth(map[string]string{"folders": "true"}))
	g.Expect(sfOptionValues(folders)).To(Equal([]string{"Exec", "Sales Reports"}), "distinct and sorted")
}

func TestSalesforceListViewsAreServedFromTheObjectEndpoint(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	var path string
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/listviews") {
			path = r.URL.RequestURI()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"listviews":[{"id":"00B5f000004AbcdEAC","label":"My Leads"}]}`))
			return true
		}
		return false
	}
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-list-views", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfOptionValues(body)).To(Equal([]string{"00B5f000004AbcdEAC"}))
	g.Expect(path).To(ContainSubstring("/sobjects/Lead/listviews"))
	g.Expect(path).To(ContainSubstring("limit=200"), "bounded like every other list")
}

func TestSalesforceObjectsHideShadowObjectsAndHonourCustomOnly(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sobjects":[
			{"name":"Account","label":"Account","queryable":true,"custom":false},
			{"name":"AccountHistory","label":"Account History","queryable":true,"custom":false},
			{"name":"AccountShare","label":"Account Share","queryable":true,"custom":false},
			{"name":"AccountFeed","label":"Account Feed","queryable":true,"custom":false},
			{"name":"OpportunityFieldHistory","label":"Opportunity Field History","queryable":true,"custom":false},
			{"name":"SecretShare","label":"Secret Share","queryable":false,"custom":false},
			{"name":"SecretShareShare","label":"Secret Share Share","queryable":true,"custom":false},
			{"name":"Lead__ChangeEvent","label":"Lead Change Event","queryable":true,"custom":false},
			{"name":"Invoice__c","label":"Invoice","queryable":true,"custom":true},
			{"name":"TimeShare__c","label":"Time Share","queryable":true,"custom":true},
			{"name":"LoginHistory","label":"Login History","queryable":true,"custom":false},
			{"name":"VerificationHistory","label":"Identity Verification History","queryable":true,"custom":false},
			{"name":"OldThing","label":"Old","queryable":true,"custom":false,"deprecatedAndHidden":true},
			{"name":"NotQueryable","label":"Nope","queryable":false,"custom":false}
		]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	// The shadow tables are a third of a real global describe and never what an
	// operator meant; a CUSTOM object that merely ends in "Share" survives.
	//
	// LoginHistory and VerificationHistory are the other half of the rule: both are
	// real, queryable, non-deprecated standard objects (verified live), and there is
	// no Login or Verification object for them to be the shadow of. A bare "History"
	// suffix rule drops both, and the operator who wanted login history is left
	// concluding Flomation cannot read it.
	all, _ := getSalesforceOptions(r, "salesforce-objects", sfAuth(nil))
	g.Expect(sfOptionValues(all)).To(ConsistOf(
		"Account", "Invoice__c", "TimeShare__c", "LoginHistory", "VerificationHistory"))

	// …and the parent test is not fooled by the parent being invisible: SecretShare
	// is not queryable, so it never appears in the picker, but SecretShareShare is
	// still recognised as its shadow. Field history is matched by name because
	// OpportunityFieldHistory trims to "OpportunityField", which is not an object.
	g.Expect(sfOptionValues(all)).ToNot(ContainElement("SecretShareShare"))
	g.Expect(sfOptionValues(all)).ToNot(ContainElement("OpportunityFieldHistory"))

	custom, _ := getSalesforceOptions(r, "salesforce-objects", sfAuth(map[string]string{"custom_only": "true"}))
	g.Expect(sfOptionValues(custom)).To(ConsistOf("Invoice__c", "TimeShare__c"))
}

// Every object the markers BAKE into an endpoint must survive the shadow-object
// filter, or the picker that lists objects and the picker scoped to one of them
// disagree about what exists.
func TestSalesforceBakedObjectsAreNotHiddenAsShadowObjects(t *testing.T) {
	g := NewWithT(t)

	// The baked names are their own parent set: if a marker ever baked both
	// "Account" and "AccountHistory", the shadow rule would fire on the second and
	// this test would say so.
	known := map[string]bool{}
	for _, object := range salesforceBakedObjects(t) {
		known[object] = true
	}
	for _, object := range salesforceBakedObjects(t) {
		g.Expect(salesforceIsSystemObject(object, strings.HasSuffix(object, "__c"), known)).
			To(BeFalse(), "%s is baked into a marker but hidden from the object picker", object)
	}
}

// ---------------------------------------------------------------------------
// 8. The describe cache — the field-level-security boundary
// ---------------------------------------------------------------------------

// THE test in this file. Describe output is filtered by the connected user's
// field-level security, so a cache key that does not include the credential
// serves one user's visible fields to another. The two credentials are given
// DIFFERENT describes, so a shared cache shows up as the wrong FIELDS, not merely
// as a missing round trip.
func TestSalesforceDescribeCacheDoesNotLeakAcrossCredentials(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)

	var mu sync.Mutex
	describes := map[string]int{}
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/describe") {
			return false
		}
		auth := r.Header.Get("Authorization")
		mu.Lock()
		describes[auth]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer token-alice":
			// Alice's profile can see the salary field.
			_, _ = w.Write([]byte(`{"fields":[
				{"name":"Name","label":"Name","type":"string","nameField":true},
				{"name":"Salary__c","label":"Salary","type":"currency"}
			]}`))
		default:
			// Bob's cannot.
			_, _ = w.Write([]byte(`{"fields":[
				{"name":"Name","label":"Name","type":"string","nameField":true}
			]}`))
		}
		return true
	}

	r := setupSalesforceRouter(&Service{})
	alice := map[string]string{
		"access_token": "token-alice",
		"instance_url": "https://acme.my.salesforce.com",
		"object":       "Employee__c",
	}
	bob := map[string]string{
		"access_token": "token-bob",
		"instance_url": "https://acme.my.salesforce.com",
		"object":       "Employee__c",
	}

	first, _ := getSalesforceOptions(r, "salesforce-fields", alice)
	g.Expect(sfOptionValues(first)).To(ConsistOf("Name", "Salary__c"))

	// Second call for the same credential is cached — the whole point of the cache.
	second, _ := getSalesforceOptions(r, "salesforce-fields", alice)
	g.Expect(sfOptionValues(second)).To(ConsistOf("Name", "Salary__c"))
	mu.Lock()
	aliceCalls := describes["Bearer token-alice"]
	mu.Unlock()
	g.Expect(aliceCalls).To(Equal(1), "the second call for the same credential should be cached")

	// A DIFFERENT credential must get its own describe, not Alice's.
	third, _ := getSalesforceOptions(r, "salesforce-fields", bob)
	g.Expect(sfOptionValues(third)).ToNot(ContainElement("Salary__c"),
		"Bob was served Alice's field-level security through the describe cache")
	g.Expect(sfOptionValues(third)).To(ConsistOf("Name"))

	mu.Lock()
	bobCalls := describes["Bearer token-bob"]
	mu.Unlock()
	g.Expect(bobCalls).To(Equal(1))

	// …and Alice must still see her own after Bob's call, in both directions.
	fourth, _ := getSalesforceOptions(r, "salesforce-fields", alice)
	g.Expect(sfOptionValues(fourth)).To(ConsistOf("Name", "Salary__c"))
}

// The same reasoning applies to the global describe, which the object picker
// caches per credential too.
func TestSalesforceGlobalDescribeCacheIsKeyedOnTheCredential(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/sobjects/") {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer token-alice" {
			_, _ = w.Write([]byte(`{"sobjects":[{"name":"Secret__c","label":"Secret","queryable":true,"custom":true}]}`))
		} else {
			_, _ = w.Write([]byte(`{"sobjects":[{"name":"Public__c","label":"Public","queryable":true,"custom":true}]}`))
		}
		return true
	}
	r := setupSalesforceRouter(&Service{})

	alice, _ := getSalesforceOptions(r, "salesforce-objects", map[string]string{
		"access_token": "token-alice", "instance_url": "https://acme.my.salesforce.com",
	})
	g.Expect(sfOptionValues(alice)).To(ConsistOf("Secret__c"))

	bob, _ := getSalesforceOptions(r, "salesforce-objects", map[string]string{
		"access_token": "token-bob", "instance_url": "https://acme.my.salesforce.com",
	})
	g.Expect(sfOptionValues(bob)).To(ConsistOf("Public__c"),
		"the global describe cache leaked one credential's object list to another")
}

// Two objects under one credential must not share an entry either.
func TestSalesforceDescribeCacheIsKeyedOnTheObject(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/describe") {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/Lead/") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"LeadOnly__c","label":"Lead Only","type":"string"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"fields":[{"name":"CaseOnly__c","label":"Case Only","type":"string"}]}`))
		}
		return true
	}
	r := setupSalesforceRouter(&Service{})

	lead, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead"}))
	g.Expect(sfOptionValues(lead)).To(ConsistOf("LeadOnly__c"))
	kase, _ := getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Case"}))
	g.Expect(sfOptionValues(kase)).To(ConsistOf("CaseOnly__c"))
}

// The cache is a map held for minutes in a process that also holds every tenant's
// secrets. The access token must not be recoverable from it.
func TestSalesforceCacheNeverStoresTheToken(t *testing.T) {
	g := NewWithT(t)
	salesforceDescribeOnlyStub(t)
	r := setupSalesforceRouter(&Service{})

	_, _ = getSalesforceOptions(r, "salesforce-fields", sfAuth(map[string]string{"object": "Lead"}))

	salesforceCacheMu.Lock()
	keys := make([]string, 0, len(salesforceCache))
	for k := range salesforceCache {
		keys = append(keys, k)
	}
	salesforceCacheMu.Unlock()

	g.Expect(keys).ToNot(BeEmpty(), "nothing was cached, so this test proves nothing")
	for _, k := range keys {
		g.Expect(k).ToNot(ContainSubstring(sfToken), "the raw token is a cache key component")
	}
}

// A dropdown cache that grows with every object any tenant has ever opened is a
// slow memory leak.
func TestSalesforceCacheIsBoundedAndExpires(t *testing.T) {
	g := NewWithT(t)
	sfClearDescribeCache()
	defer sfClearDescribeCache()

	for i := 0; i < salesforceCacheMaxSize*3; i++ {
		salesforceCachePut(strings.Repeat("k", i%7)+string(rune('a'+i%26))+strings.Repeat("x", i), i)
	}
	salesforceCacheMu.Lock()
	size := len(salesforceCache)
	salesforceCacheMu.Unlock()
	g.Expect(size).To(BeNumerically("<=", salesforceCacheMaxSize))

	// An expired entry is never served, however recently it was put.
	sfClearDescribeCache()
	salesforceCacheMu.Lock()
	salesforceCache["stale"] = salesforceCacheEntry{value: 1, expires: time.Now().Add(-time.Second)}
	salesforceCacheMu.Unlock()
	_, ok := salesforceCacheGet("stale")
	g.Expect(ok).To(BeFalse())
}

// The cache alone does not stop duplicate work: an entry is only written once a
// describe has COME BACK, so nothing stops N goroutines being partway through the
// same describe. That is the normal case, not a rare race — the editor fetches
// every dynamic option on mount, so opening one node fires all of its pickers at
// once and task_update alone describes Task eight times over (~120 KB each,
// every one of them counted against the customer's daily API allowance).
func TestSalesforceConcurrentDescribesOfOneObjectMakeOneCall(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)

	var describes int32
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/describe") {
			return false
		}
		atomic.AddInt32(&describes, 1)
		// Wide enough that every caller released from the barrier below is inside
		// salesforceDescribeObject before this one returns.
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(salesforceDescribeStub))
		return true
	}

	r := setupSalesforceRouter(&Service{})

	// The eight pickers a single task_update node opens against Task.
	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body, code := getSalesforceOptions(r, "salesforce-picklist", sfAuth(map[string]string{
				"object": "Task", "field": "Status",
			}))
			g.Expect(code).To(Equal(http.StatusOK))
			// Every waiter still gets the real answer, not an empty list.
			g.Expect(sfOptionValues(body)).To(ContainElement("Working - Contacted"))
		}()
	}
	close(start)
	wg.Wait()

	g.Expect(atomic.LoadInt32(&describes)).To(Equal(int32(1)),
		"%d concurrent pickers on one object produced %d describes", callers, describes)
}

// A follower must never inherit the leader's failure. Every outbound call is
// bound to its own request's context, so the leader failing can mean nothing
// worse than "that operator closed the config panel" — sharing that error would
// blank six live dropdowns. On failure each waiter does the work itself.
func TestSalesforceInFlightFollowersDoNotInheritTheLeadersFailure(t *testing.T) {
	g := NewWithT(t)

	leaderInside := make(chan struct{})
	release := make(chan struct{})
	var calls int32

	fetch := func() (any, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(leaderInside)
			<-release
			return nil, errors.New("leader's request was cancelled")
		}
		return "the real answer", nil
	}

	type outcome struct {
		value any
		err   error
	}
	leaderDone := make(chan outcome, 1)
	followerDone := make(chan outcome, 1)

	go func() {
		v, err := salesforceFetchOnce(context.Background(), "k", fetch)
		leaderDone <- outcome{v, err}
	}()
	<-leaderInside

	followerStarted := make(chan struct{})
	go func() {
		close(followerStarted)
		v, err := salesforceFetchOnce(context.Background(), "k", fetch)
		followerDone <- outcome{v, err}
	}()
	<-followerStarted
	// Let the follower reach the in-flight entry before the leader clears it.
	time.Sleep(50 * time.Millisecond)
	close(release)

	leader := <-leaderDone
	g.Expect(leader.err).To(HaveOccurred(), "the leader is meant to fail in this test")

	follower := <-followerDone
	g.Expect(follower.err).ToNot(HaveOccurred(), "the follower inherited the leader's failure")
	g.Expect(follower.value).To(Equal("the real answer"))
	g.Expect(atomic.LoadInt32(&calls)).To(Equal(int32(2)))
}

// ---------------------------------------------------------------------------
// 9. Secret and managed-credential resolution
// ---------------------------------------------------------------------------

const sfEnvID = "00000000-0000-0000-0000-000000000001"

// sfMockPersistence adds the managed-credential lookup the Salesforce Connect
// path needs on top of the shared environment mock.
type sfMockPersistence struct {
	*mockPersistence
	credName  string
	credToken string
	credMeta  string
}

func (m *sfMockPersistence) GetCredentialWithMetaByName(_ string, name string, _ string) (*string, *json.RawMessage, error) {
	if name != m.credName {
		return nil, nil, nil
	}
	token := m.credToken
	if m.credMeta == "" {
		return &token, nil, nil
	}
	meta := json.RawMessage(m.credMeta)
	return &token, &meta, nil
}

func newSFMockPersistence() *sfMockPersistence {
	base := newMockPersistence()
	// A user with no organisations is in "personal mode", where checkPermission
	// grants every permission — so these tests exercise resolution, not RBAC.
	base.users["user-1"] = &api.User{ID: "user-1"}
	base.environments[sfEnvID] = &api.Environment{ID: sfEnvID, Name: "production", SecretKey: "key123"}
	return &sfMockPersistence{mockPersistence: base}
}

// A ${credentials.X} reference from the "Connect Salesforce" flow must RESOLVE,
// not be refused: if the managed path had no dropdowns it would be worse UX than
// a pasted token, which is the wrong way round.
func TestSalesforceManagedCredentialResolves(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)

	mock := newSFMockPersistence()
	mock.credName = "SALESFORCE_PROD"
	mock.credToken = "managed-access-token"
	mock.credMeta = `{"instance_url":"https://acme.my.salesforce.com"}`

	r := setupSalesforceRouter(&Service{persistence: mock})
	body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${credentials.SALESFORCE_PROD}",
		// The editor cannot resolve this, so the credential's own captured value
		// has to be used instead.
		"instance_url": "${credentials.SALESFORCE_PROD.instance_url}",
		"environment":  sfEnvID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	_, auths := stub.recorded()
	g.Expect(auths).To(HaveLen(1))
	g.Expect(auths[0]).To(Equal("Bearer managed-access-token"),
		"the managed credential must be resolved server-side and sent as the bearer token")
}

// The instance URL captured on a managed credential is still attacker-reachable
// (an OAuth response, or a tampered credentials row), so it goes through exactly
// the same validation as a pasted one.
func TestSalesforceManagedCredentialInstanceURLIsStillValidated(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)

	mock := newSFMockPersistence()
	mock.credName = "SALESFORCE_PROD"
	mock.credToken = "managed-access-token"
	mock.credMeta = `{"instance_url":"https://evil.example"}`

	r := setupSalesforceRouter(&Service{persistence: mock})
	body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${credentials.SALESFORCE_PROD}",
		"instance_url": "${credentials.SALESFORCE_PROD.instance_url}",
		"environment":  sfEnvID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).ToNot(BeEmpty())
	g.Expect(rt.seen()).To(BeEmpty(), "a managed credential's own instance_url reached the network unvalidated")

	// And the happy managed path really does go to the org's host with the token,
	// so the assertion above is not vacuous.
	rt.reset()
	mock.credMeta = `{"instance_url":"https://acme.my.salesforce.com"}`
	_, code = getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${credentials.SALESFORCE_PROD}",
		"instance_url": "${credentials.SALESFORCE_PROD.instance_url}",
		"environment":  sfEnvID,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	seen := rt.seen()
	g.Expect(seen).To(HaveLen(1))
	g.Expect(seen[0].Host).To(Equal("acme.my.salesforce.com"))
	g.Expect(seen[0].Auth).To(Equal("Bearer managed-access-token"))
}

func TestSalesforceReferencesWithoutAnEnvironmentAreExplained(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)
	r := setupSalesforceRouter(&Service{persistence: newSFMockPersistence()})

	for _, token := range []string{"${credentials.SALESFORCE_PROD}", "${secrets.SF_TOKEN}"} {
		rt.reset()
		body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
			"access_token": token,
			"instance_url": "https://acme.my.salesforce.com",
		})
		g.Expect(code).To(Equal(http.StatusOK), token)
		g.Expect(sfError(body)).To(ContainSubstring("environment"), token)
		g.Expect(rt.seen()).To(BeEmpty(), token)
	}

	// A credential that is not set up in the environment is a sentence, not a 500.
	rt.reset()
	body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${credentials.MISSING}",
		"instance_url": "https://acme.my.salesforce.com",
		"environment":  sfEnvID,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("reconnect Salesforce"))
	g.Expect(rt.seen()).To(BeEmpty())
}

// A pasted ${secrets.X} token is resolved server-side; the plaintext never
// transits the browser.
func TestSalesforceSecretRefIsResolvedServerSide(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)

	mock := newSFMockPersistence()
	mock.secrets[sfEnvID+"/SF_TOKEN"] = &api.EnvironmentSecret{
		EnvironmentID: sfEnvID, Name: "SF_TOKEN", Value: "resolved-session-id",
	}

	r := setupSalesforceRouter(&Service{persistence: mock})
	body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${secrets.SF_TOKEN}",
		"instance_url": "https://acme.my.salesforce.com",
		"environment":  sfEnvID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	_, auths := stub.recorded()
	g.Expect(auths).To(ConsistOf("Bearer resolved-session-id"))

	// A secret that is not there is a sentence, and the name is never guessed at.
	body, code = getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": "${secrets.NOT_THERE}",
		"instance_url": "https://acme.my.salesforce.com",
		"environment":  sfEnvID,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("not found in environment"))
}

// The instance URL may itself be a secret reference, and resolving it is the same
// privileged operation as resolving the token.
func TestSalesforceSecretInstanceURLIsResolvedAndValidated(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)

	mock := newSFMockPersistence()
	mock.secrets[sfEnvID+"/SF_HOST"] = &api.EnvironmentSecret{
		EnvironmentID: sfEnvID, Name: "SF_HOST", Value: "https://acme.my.salesforce.com",
	}
	mock.secrets[sfEnvID+"/SF_EVIL"] = &api.EnvironmentSecret{
		EnvironmentID: sfEnvID, Name: "SF_EVIL", Value: "https://evil.example",
	}

	r := setupSalesforceRouter(&Service{persistence: mock})

	_, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": sfToken,
		"instance_url": "${secrets.SF_HOST}",
		"environment":  sfEnvID,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(rt.seen()).To(HaveLen(1))
	g.Expect(rt.seen()[0].Host).To(Equal("acme.my.salesforce.com"))

	rt.reset()
	body, code := getSalesforceOptions(r, "salesforce-users", map[string]string{
		"access_token": sfToken,
		"instance_url": "${secrets.SF_EVIL}",
		"environment":  sfEnvID,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).ToNot(BeEmpty())
	g.Expect(rt.seen()).To(BeEmpty(), "a secret-held instance URL bypassed validation")
}

// Resolving a secret to plaintext is gated on environment.view: the resolved
// value authenticates a request to a caller-influenced host, so a member denied
// that permission could otherwise read any secret through a dropdown.
//
// This is the ONE deliberate exception to the always-200 contract — it is an
// authorisation failure, not an input the operator can fix — so the test pins the
// 403 as well as the "nothing left the process" property.
func TestSalesforceSecretResolutionIsGatedOnEnvironmentView(t *testing.T) {
	g := NewWithT(t)
	rt := sfIntercept(t)

	mock := newSFMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.secrets[sfEnvID+"/SF_TOKEN"] = &api.EnvironmentSecret{
		EnvironmentID: sfEnvID, Name: "SF_TOKEN", Value: "resolved-session-id",
	}
	denied := &sfDeniedPersistence{sfMockPersistence: mock}

	gin.SetMode(gin.TestMode)
	svc := &Service{persistence: denied}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Set("organisation_id", "org-1")
		c.Next()
	})
	engine.GET("/api/v1/action/options/salesforce-users", svc.getSalesforceUsers)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/action/options/salesforce-users?"+url.Values{
			"access_token": {"${secrets.SF_TOKEN}"},
			"instance_url": {"https://acme.my.salesforce.com"},
			"environment":  {sfEnvID},
		}.Encode(), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	g.Expect(rec.Code).To(Equal(http.StatusForbidden))
	g.Expect(rec.Body.String()).ToNot(ContainSubstring("resolved-session-id"))
	g.Expect(rt.seen()).To(BeEmpty(), "a user without environment.view resolved a secret and used it")
}

// sfDeniedPersistence puts the user in an organisation that HAS groups but has
// not put the user in any of them — rbac's "intentionally no permissions" state.
type sfDeniedPersistence struct {
	*sfMockPersistence
}

func (m *sfDeniedPersistence) GetUserRoleInOrganisation(string, string) (*string, error) {
	member := "member"
	return &member, nil
}

func (m *sfDeniedPersistence) CountUserGroupsInOrganisation(string, string) (int, error) {
	return 0, nil
}

func (m *sfDeniedPersistence) GetGroupsByOrganisationID(string) ([]*api.Group, error) {
	return []*api.Group{{ID: "group-1", Name: "engineers"}}, nil
}

// ---------------------------------------------------------------------------
// 10. Error contract
// ---------------------------------------------------------------------------

// Salesforce's vocabulary is not the operator's. Every upstream failure becomes a
// sentence that says what to do; the raw errorCode is logged, never returned.
func TestSalesforceUpstreamErrorsAreOperatorProse(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		wantPhrase  string
		neverLeaked string
	}{
		{
			name: "expired session", status: http.StatusUnauthorized,
			body:        `[{"message":"Session expired or invalid","errorCode":"INVALID_SESSION_ID"}]`,
			wantPhrase:  "reconnect Salesforce",
			neverLeaked: "INVALID_SESSION_ID",
		},
		{
			name: "a 200-with-INVALID_SESSION_ID body is still a 4xx in disguise", status: http.StatusBadRequest,
			body:        `[{"message":"Session expired or invalid","errorCode":"INVALID_SESSION_ID"}]`,
			wantPhrase:  "reconnect Salesforce",
			neverLeaked: "INVALID_SESSION_ID",
		},
		{
			name: "no permission", status: http.StatusForbidden,
			body:        `[{"message":"insufficient access rights","errorCode":"INSUFFICIENT_ACCESS"}]`,
			wantPhrase:  "Salesforce administrator",
			neverLeaked: "INSUFFICIENT_ACCESS",
		},
		{
			name: "unknown object", status: http.StatusNotFound,
			body:        `[{"message":"The requested resource does not exist","errorCode":"NOT_FOUND"}]`,
			wantPhrase:  "check the Object",
			neverLeaked: "NOT_FOUND",
		},
		{
			// Salesforce returns an exhausted API allowance as 403, the same status
			// as a permissions failure. A status-first switch tells the operator to
			// go and ask their administrator for access, when the actual fix is to
			// wait — so the errorCode has to win.
			name: "api limit", status: http.StatusForbidden,
			body:        `[{"message":"TotalRequests Limit exceeded.","errorCode":"REQUEST_LIMIT_EXCEEDED"}]`,
			wantPhrase:  "API request limit",
			neverLeaked: "REQUEST_LIMIT_EXCEEDED",
		},
		{
			name: "read-only access", status: http.StatusBadRequest,
			body:        `[{"message":"entity is read-only","errorCode":"INSUFFICIENT_ACCESS_OR_READONLY"}]`,
			wantPhrase:  "Salesforce administrator",
			neverLeaked: "INSUFFICIENT_ACCESS_OR_READONLY",
		},
		{
			name: "query timeout", status: http.StatusBadRequest,
			body:        `[{"message":"Your query request was running for too long","errorCode":"QUERY_TIMEOUT"}]`,
			wantPhrase:  "took too long",
			neverLeaked: "QUERY_TIMEOUT",
		},
		{
			name: "unsupported object type", status: http.StatusBadRequest,
			body:        `[{"message":"sObject type 'Widget' is not supported","errorCode":"INVALID_TYPE"}]`,
			wantPhrase:  "doesn't recognise that object",
			neverLeaked: "INVALID_TYPE",
		},
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			body:        `{"error":"rate_limited"}`,
			wantPhrase:  "rate-limiting",
			neverLeaked: "rate_limited",
		},
		{
			name: "server error", status: http.StatusInternalServerError,
			body:        `[{"message":"internal","errorCode":"UNKNOWN_EXCEPTION"}]`,
			wantPhrase:  "type the value in",
			neverLeaked: "UNKNOWN_EXCEPTION",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			stub := newSalesforceStub(t)
			stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
				return true
			}
			r := setupSalesforceRouter(&Service{})

			body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))
			g.Expect(code).To(Equal(http.StatusOK), "never a 4xx — the editor shows the message inline")
			g.Expect(sfError(body)).To(ContainSubstring(tc.wantPhrase))
			g.Expect(sfError(body)).ToNot(ContainSubstring(tc.neverLeaked),
				"the raw Salesforce error code means nothing to a receptionist")
			g.Expect(sfError(body)).ToNot(ContainSubstring(sfToken))
		})
	}
}

// Salesforce overloads its HTTP statuses — the same 403 carries "you have no
// permission" and "your org is out of API calls", which need opposite advice. The
// org's own errorCode therefore has to be matched before the status bucket.
func TestSalesforceErrorCodeBeatsTheHTTPStatus(t *testing.T) {
	g := NewWithT(t)

	limit := &salesforceStatusError{status: http.StatusForbidden, code: "REQUEST_LIMIT_EXCEEDED", message: "TotalRequests Limit exceeded."}
	g.Expect(salesforceErrorMessage(limit, "list of users")).To(ContainSubstring("API request limit"))
	g.Expect(salesforceErrorMessage(limit, "list of users")).ToNot(ContainSubstring("administrator"))

	// A session that expired mid-request can come back as 403 too.
	expired := &salesforceStatusError{status: http.StatusForbidden, code: "INVALID_SESSION_ID"}
	g.Expect(salesforceErrorMessage(expired, "list of users")).To(ContainSubstring("reconnect Salesforce"))

	// With no errorCode at all the status is still enough to say something useful.
	g.Expect(salesforceErrorMessage(&salesforceStatusError{status: http.StatusForbidden}, "list of users")).
		To(ContainSubstring("administrator"))
	g.Expect(salesforceErrorMessage(&salesforceStatusError{status: http.StatusUnauthorized}, "list of users")).
		To(ContainSubstring("reconnect Salesforce"))
	g.Expect(salesforceErrorMessage(&salesforceStatusError{status: http.StatusTooManyRequests}, "list of users")).
		To(ContainSubstring("rate-limiting"))
	g.Expect(salesforceErrorMessage(&salesforceStatusError{status: http.StatusBadGateway}, "list of users")).
		To(ContainSubstring("HTTP 502"))
}

// An org that simply has none of something is not an error: the editor must get
// an empty list (and its free-text row), not a red message.
func TestSalesforceEmptyResultsAreAnEmptyListNotAnError(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		if strings.Contains(r.URL.Path, "/listviews") {
			_, _ = w.Write([]byte(`{"listviews":[]}`))
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/sobjects/") {
			_, _ = w.Write([]byte(`{"sobjects":[]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	for _, slug := range []string{
		"salesforce-objects", "salesforce-lookup", "salesforce-users",
		"salesforce-list-views", "salesforce-reports", "salesforce-campaign-member-status",
		"salesforce-price-book-entries",
	} {
		params := sfAuth(sfRequiredExtras(slug))
		body, code := getSalesforceOptions(r, slug, params)
		g.Expect(code).To(Equal(http.StatusOK), slug)
		g.Expect(body).ToNot(HaveKey("error"), slug)
		g.Expect(body).To(HaveKey("options"), slug)
		g.Expect(sfOptionValues(body)).To(BeEmpty(), slug)
	}
}

// A search box the operator never typed in still arrives as the editor's raw
// binding. Sending "${node.x.output.q}" into a LIKE returns nothing and looks
// like a broken picker, so it is treated as no search at all.
func TestSalesforceLookupIgnoresAnUnresolvedSearchTerm(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(`{"fields":[{"name":"Name","label":"Name","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"0015f00000AbCdEAAV","Name":"Acme"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{
		"object": "Account", "search": "${node.abc.output.q}",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfOptionValues(body)).To(ConsistOf("0015f00000AbCdEAAV"))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("WHERE"))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("${"))
}

func TestSalesforceUnreachableOrgIsAnOperatorSentence(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.server.Close() // nothing listening
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Could not reach Salesforce"))
	// A transport error string can contain the URL; it must not contain the token.
	g.Expect(sfError(body)).ToNot(ContainSubstring(sfToken))
}

// A body that is not JSON at all (a proxy's login page, an HTML error) must not
// panic or 500 the picker.
func TestSalesforceNonJSONResponseIsAnOperatorSentence(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Sign in to your corporate proxy</body></html>"))
		return true
	}
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).ToNot(BeEmpty())
}

// ---------------------------------------------------------------------------
// 11. Transport hardening
// ---------------------------------------------------------------------------

// Salesforce is SaaS: an org is never on a metadata, link-local, loopback or
// RFC1918 address, so a *.salesforce.com name that resolves there is DNS
// rebinding or a misconfiguration — neither worth following from a process that
// holds every tenant's secrets.
func TestSalesforceDialControlRefusesInternalDestinations(t *testing.T) {
	g := NewWithT(t)

	prev := salesforceOptionsHostOverride
	salesforceOptionsHostOverride = ""
	defer func() { salesforceOptionsHostOverride = prev }()

	for _, addr := range []string{
		"169.254.169.254:80", // AWS/Azure/GCP IMDS
		"[fd00:ec2::254]:80", // AWS IMDS over IPv6
		"100.100.100.200:80", // Alibaba Cloud
		"[fe80::1]:443",      // link-local
		"127.0.0.1:8888",     // the api's own port
		"10.0.0.5:443",
		"172.16.4.4:443",
		"192.168.1.169:443",
		"0.0.0.0:443",
	} {
		g.Expect(salesforceOptionsDialControl("tcp", addr, nil)).To(HaveOccurred(), addr)
	}
	// A real Salesforce pod address is fine.
	g.Expect(salesforceOptionsDialControl("tcp", "85.222.128.1:443", nil)).ToNot(HaveOccurred())

	// Under the test seam loopback is reachable again, or no proxy test could run.
	salesforceOptionsHostOverride = "http://127.0.0.1:1234"
	g.Expect(salesforceOptionsDialControl("tcp", "127.0.0.1:1234", nil)).ToNot(HaveOccurred())
	// …but never the metadata service, seam or no seam.
	g.Expect(salesforceOptionsDialControl("tcp", "169.254.169.254:80", nil)).To(HaveOccurred())
}

// The Control has to be WIRED INTO the shared client, not merely correct in
// isolation — a refactor that adds a proxy, swaps in a shared dialer or moves the
// client to a helper drops it silently.
//
// The assertion has to be about the guard's own error, not about the handler's
// prose. "Could not reach Salesforce" is what salesforceErrorMessage returns for
// ANY non-status failure, so a version of this test that only checked the
// sentence passed with `Control: salesforceOptionsDialControl` deleted: on a box
// where 169.254.169.254 is simply unroutable the dial failed anyway, with
// "connect: network is unreachable", and the test went green. On a cloud runner —
// the one place the guard actually matters — the metadata service would have
// answered instead. A security test that cannot fail is worse than no test.
//
// So the dial is driven through the CLIENT'S OWN Transport and the guard's
// message demanded. That fails both ways: without the Control the error is a
// connect error on this box, and no error at all where IMDS is reachable.
func TestSalesforceMetadataServiceIsUnreachableThroughTheRealTransport(t *testing.T) {
	g := NewWithT(t)

	prev := salesforceOptionsHostOverride
	salesforceOptionsHostOverride = "http://169.254.169.254"
	sfClearDescribeCache()
	defer func() {
		salesforceOptionsHostOverride = prev
		sfClearDescribeCache()
	}()

	transport, isTransport := salesforceOptionsHTTPClient.Transport.(*http.Transport)
	g.Expect(isTransport).To(BeTrue(),
		"the shared client no longer dials through an *http.Transport, so nothing pins the dial guard")
	g.Expect(transport.DialContext).ToNot(BeNil(),
		"the shared client dials with the default dialer, which carries no Control")

	// Seam or no seam, the metadata service is refused BY THE GUARD.
	_, err := transport.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	g.Expect(err).To(HaveOccurred(), "the metadata service was dialled")
	g.Expect(err.Error()).To(ContainSubstring("link-local addresses are not allowed"),
		"the dial failed, but not because of the guard — the Control is not wired into the client")

	// And with the seam off, so is loopback: no Salesforce org is ever there, so a
	// *.salesforce.com name that resolves to it is rebinding or misconfiguration.
	salesforceOptionsHostOverride = ""
	_, err = transport.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("loopback addresses are not allowed"))
	salesforceOptionsHostOverride = "http://169.254.169.254"

	// End to end the operator still gets a sentence, never dial detail.
	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Could not reach Salesforce"))
	g.Expect(sfError(body)).ToNot(ContainSubstring("169.254.169.254"))
}

// A 302 must never carry the bearer token to another host.
func TestSalesforceCrossHostRedirectCannotCarryTheToken(t *testing.T) {
	g := NewWithT(t)

	var elsewhereHits int
	var elsewhereAuth string
	var mu sync.Mutex
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhereHits++
		elsewhereAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	defer elsewhere.Close()

	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-users", sfAuth(nil))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Could not reach Salesforce"))

	mu.Lock()
	hits, auth := elsewhereHits, elsewhereAuth
	mu.Unlock()
	g.Expect(hits).To(Equal(0), "a redirect carried the request to another host")
	g.Expect(auth).To(BeEmpty())
}

// ---------------------------------------------------------------------------
// 11b. The relationship-traversed secondary label
// ---------------------------------------------------------------------------

// sfContractDescribe is a Contract describe: an auto-number name field and an
// Account lookup, which is the shape that makes the traversal necessary.
const sfContractDescribe = `{"fields":[
  {"name":"ContractNumber","label":"Contract Number","type":"string","nameField":true,"filterable":true,"sortable":true},
  {"name":"AccountId","label":"Account ID","type":"reference","relationshipName":"Account","filterable":true,"sortable":true},
  {"name":"Status","label":"Status","type":"picklist"}
]}`

// Contract's name field is an AUTO-NUMBER — ContractNumber, "00000109" — so a
// picker built from it alone asks a receptionist to choose between six-digit
// numbers with nothing to tell them apart. The customer's name is the only thing
// that identifies a contract and it lives one relationship away, so the secondary
// label column has to be able to traverse.
func TestSalesforceLookupLabelsAnAutoNumberWithTheParentsName(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(sfContractDescribe))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[
          {"Id":"800aj000039Qi57AAC","ContractNumber":"00000109","Account":{"Name":"Burlington Textiles"}},
          {"Id":"800aj000039Qi58AAC","ContractNumber":"00000110","Account":null}
        ]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{"object": "Contract"}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())
	// The traversal is in the SELECT list, and only because the describe confirmed
	// the relationship.
	g.Expect(stub.soql()).To(HaveLen(1))
	g.Expect(stub.soql()[0]).To(ContainSubstring("SELECT Id, ContractNumber, Account.Name FROM Contract"))
	// Salesforce returns a traversed column NESTED, so a flat record["Account.Name"]
	// read would silently produce a list of bare numbers.
	g.Expect(sfOptionNames(body)).To(ConsistOf("00000109 — Burlington Textiles", "00000110"))
}

// A null lookup comes back as JSON null rather than an empty object, and an
// account the org has since deleted must not blank the row.
func TestSalesforceLookupTraversalIsOnlySelectedWhenTheDescribeConfirmsIt(t *testing.T) {
	g := NewWithT(t)
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			// No AccountId at all — a user whose field-level security hides it, which
			// is exactly when an unguarded SELECT would become INVALID_FIELD on every
			// dropdown open.
			_, _ = w.Write([]byte(`{"fields":[{"name":"ContractNumber","label":"Number","type":"string","nameField":true,"filterable":true,"sortable":true}]}`))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[{"Id":"800aj000039Qi57AAC","ContractNumber":"00000109"}]}`))
		return true
	}

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-lookup", sfAuth(map[string]string{"object": "Contract"}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("Account.Name"))
	g.Expect(sfOptionNames(body)).To(ConsistOf("00000109"))
}

// The resolver is the only thing standing between a table entry and a SELECT
// list, so its refusals are pinned directly.
func TestSalesforceSecondaryLabelResolverRefusesWhatItCannotConfirm(t *testing.T) {
	g := NewWithT(t)
	describe := &salesforceDescribe{Fields: []salesforceDescribeField{
		{Name: "ContractNumber"},
		{Name: "AccountId", RelationshipName: "Account"},
	}}

	g.Expect(salesforceResolveSecondaryLabel(describe, "ContractNumber").Path).To(Equal("ContractNumber"))

	traversed := salesforceResolveSecondaryLabel(describe, "Account.Name")
	g.Expect(traversed.Path).To(Equal("Account.Name"))
	g.Expect(traversed.Relationship).To(Equal("Account"))
	g.Expect(traversed.Leaf).To(Equal("Name"))

	for _, candidate := range []string{
		"",                     // nothing asked for
		"Owner.Name",           // relationship the describe does not carry
		"Missing",              // field the describe does not carry
		"Account.Owner.Name",   // more than one hop
		"Account.Name, Secret", // not an identifier
		"Account.Name'--",
	} {
		g.Expect(salesforceResolveSecondaryLabel(describe, candidate).Path).
			To(BeEmpty(), "resolved %q, which would go straight into a SELECT list", candidate)
	}
}

// ---------------------------------------------------------------------------
// 11c. Price book entries (two-hop)
// ---------------------------------------------------------------------------

// A price book ENTRY is the id nobody has: it is the join between a price book
// and a product, and Salesforce demands it before a product can go on a quote,
// an order or an opportunity. The generic lookup cannot label one — PricebookEntry
// flags the PRODUCT's name as its name field, so a product priced in two books is
// two rows reading identically. These tests pin the three things that make the
// dedicated picker worth having: the parent hop, the scoping, and the labels.

// sfPricebookEntryDescribe is a single-currency org's PricebookEntry. Selecting
// CurrencyIsoCode against one of these is a hard INVALID_FIELD (verified live:
// "No such column 'CurrencyIsoCode' on entity 'PricebookEntry'").
const sfPricebookEntryDescribe = `{"fields":[
  {"name":"Id","type":"id"},
  {"name":"Name","label":"Product Name","type":"string","nameField":true,"filterable":true,"sortable":true},
  {"name":"UnitPrice","label":"List Price","type":"currency"},
  {"name":"IsActive","label":"Active","type":"boolean"}
]}`

// sfPriceBookEntryStub answers the describe and then hands each /query the next
// canned body in turn, so a test can assert the ORDER of the two hops.
func sfPriceBookEntryStub(t *testing.T, describe string, bodies ...string) *salesforceStub {
	t.Helper()
	stub := newSalesforceStub(t)
	var calls int
	var mu sync.Mutex
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(describe))
			return true
		}
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if n < len(bodies) {
			_, _ = w.Write([]byte(bodies[n]))
			return true
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
		return true
	}
	return stub
}

const sfTwoEntriesOneBook = `{"records":[
  {"Id":"01uaj000008Qi5mAAC","Name":"GenWatt Diesel 1000kW","UnitPrice":100000.0,"IsActive":true,
   "Pricebook2":{"Name":"Standard"},"Product2":{"Name":"GenWatt Diesel 1000kW","ProductCode":"GC1060"}},
  {"Id":"01uaj000008Qi5fAAC","Name":"GenWatt Diesel 10kW","UnitPrice":5000.0,"IsActive":true,
   "Pricebook2":{"Name":"Standard"},"Product2":{"Name":"GenWatt Diesel 10kW","ProductCode":"GC1020"}}
]}`

// The whole point of the picker: the operator has chosen a quote, and the products
// they may add are the ones priced on THAT quote's price book. Anything else is a
// row Salesforce refuses, so the parent's book is read first and the entry query
// is scoped to it.
func TestSalesforcePriceBookEntriesScopeToTheParentsPriceBook(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe,
		`{"records":[{"Pricebook2Id":"01saj00000LXMP3AAP"}]}`,
		sfTwoEntriesOneBook)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"scope": "quote", "quote_id": "0Q0aj000002vHcTCAU",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())

	soql := stub.soql()
	g.Expect(soql).To(HaveLen(2))
	// Hop one: the quote names its book. The object comes from the closed scope
	// table, never from the request.
	g.Expect(soql[0]).To(Equal("SELECT Pricebook2Id FROM Quote WHERE Id = '0Q0aj000002vHcTCAU' LIMIT 1"))
	// Hop two: bounded, active-only, and pinned to the book hop one returned.
	g.Expect(soql[1]).To(ContainSubstring("FROM PricebookEntry"))
	g.Expect(soql[1]).To(ContainSubstring("IsActive = true"))
	g.Expect(soql[1]).To(ContainSubstring("Pricebook2Id = '01saj00000LXMP3AAP'"))
	g.Expect(soql[1]).To(ContainSubstring("LIMIT 100"))
	// Single-currency org: CurrencyIsoCode is not on the object, so asking for it
	// would fail the whole query.
	g.Expect(soql[1]).ToNot(ContainSubstring("CurrencyIsoCode"))

	// Labelled by product, code and price. The book is NOT repeated on every row
	// when the whole list is one book.
	g.Expect(sfOptionNames(body)).To(ConsistOf(
		"GenWatt Diesel 1000kW (GC1060) — 100000",
		"GenWatt Diesel 10kW (GC1020) — 5000",
	))
	g.Expect(sfOptionValues(body)).To(ConsistOf("01uaj000008Qi5mAAC", "01uaj000008Qi5fAAC"))
}

// Unscoped — Change Product Price has no sibling to go on — the list spans every
// book, so each row has to say which one it is from. This is the exact defect the
// generic lookup has: without the prefix these two rows read identically.
func TestSalesforcePriceBookEntriesNameTheBookWhenTheListSpansSeveral(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, `{"records":[
      {"Id":"01uaj000008Qi5mAAC","Name":"GenWatt Diesel 1000kW","UnitPrice":100000.0,"IsActive":true,
       "Pricebook2":{"Name":"Standard"},"Product2":{"Name":"GenWatt Diesel 1000kW","ProductCode":"GC1060"}},
      {"Id":"01uaj000008Qi63AAC","Name":"GenWatt Diesel 1000kW","UnitPrice":95000.0,"IsActive":false,
       "Pricebook2":{"Name":"Reseller"},"Product2":{"Name":"GenWatt Diesel 1000kW","ProductCode":"GC1060"}}
    ]}`)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"include_inactive": "true",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	// No parent to resolve, so there is exactly one query.
	g.Expect(stub.soql()).To(HaveLen(1))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("IsActive = true"))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("Pricebook2Id ="))
	g.Expect(sfOptionNames(body)).To(ConsistOf(
		"Standard: GenWatt Diesel 1000kW (GC1060) — 100000",
		// Retired prices are kept here — reactivating one is what the action is for —
		// and marked, or the operator cannot tell which row is which.
		"Reseller: GenWatt Diesel 1000kW (GC1060) — 95000 (inactive)",
	))
}

// An inactive entry is offered by Salesforce and then refused on write, the same
// defect as a retired picklist value or a deactivated user. It is dropped
// everywhere except the one action that exists to bring it back.
func TestSalesforcePriceBookEntriesDropRetiredPricesByDefault(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, `{"records":[]}`)

	r := setupSalesforceRouter(&Service{})
	_, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(nil))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(ContainSubstring("WHERE IsActive = true"))
}

// A multi-currency org's "100000" beside a product does not say which currency it
// is in, so the code is shown — but ONLY where the object really has one.
func TestSalesforcePriceBookEntriesShowTheCurrencyWhenTheOrgHasOne(t *testing.T) {
	g := NewWithT(t)
	describe := `{"fields":[
      {"name":"Name","type":"string","nameField":true},
      {"name":"UnitPrice","type":"currency"},
      {"name":"CurrencyIsoCode","label":"Currency","type":"picklist"}
    ]}`
	stub := sfPriceBookEntryStub(t, describe, `{"records":[
      {"Id":"01uaj000008Qi5mAAC","UnitPrice":100000.0,"IsActive":true,"CurrencyIsoCode":"GBP",
       "Pricebook2":{"Name":"Standard"},"Product2":{"Name":"GenWatt Diesel 1000kW"}}
    ]}`)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(nil))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(ContainSubstring("CurrencyIsoCode"))
	g.Expect(sfOptionNames(body)).To(ConsistOf("Standard: GenWatt Diesel 1000kW — 100000 GBP"))
}

// Until the operator has chosen the quote there is nothing to scope by, and
// guessing is worse than asking: the org is never called at all.
func TestSalesforcePriceBookEntriesAskForTheParentBeforeCallingTheOrg(t *testing.T) {
	g := NewWithT(t)

	for _, params := range []map[string]string{
		{"scope": "quote"}, // not chosen yet
		{"scope": "quote", "quote_id": "${node.abc.output.id}"}, // an unresolved binding
		{"scope": "order", "order_id": ""},                      // cleared
		{"scope": "opportunity", "opportunity_id": "   "},
	} {
		stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)
		r := setupSalesforceRouter(&Service{})
		body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(params))

		g.Expect(code).To(Equal(http.StatusOK), "%v", params)
		g.Expect(sfError(body)).To(ContainSubstring("its price book"), "%v", params)
		g.Expect(body).ToNot(HaveKey("options"), "%v", params)
		g.Expect(stub.soql()).To(BeEmpty(), "%v: the org was called before the parent was known", params)
	}
}

// The parent id lands in a SOQL literal, so it is matched against the record-id
// pattern before it gets anywhere near one — the same guard the campaign
// member-status picker applies.
func TestSalesforcePriceBookEntriesRejectACraftedParentID(t *testing.T) {
	g := NewWithT(t)

	for _, crafted := range []string{
		"0Q0aj000002vHcT' OR Id != '",
		"0Q0aj000002vHcTCAU OR 1=1",
		"short",
		"0Q0aj000002vHcTCAU-toolong",
	} {
		stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)
		r := setupSalesforceRouter(&Service{})
		body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
			"scope": "quote", "quote_id": crafted,
		}))

		g.Expect(code).To(Equal(http.StatusOK), crafted)
		g.Expect(sfError(body)).To(ContainSubstring("doesn't look like a Salesforce record ID"), crafted)
		g.Expect(stub.soql()).To(BeEmpty(), "%q reached the org", crafted)
	}
}

// The scope is OUR parameter, baked into the marker endpoint and never typed by an
// operator, so anything outside the closed set is refused before a connection is
// even resolved.
func TestSalesforcePriceBookEntriesRefuseAnUnknownScope(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"scope": "Contract WHERE Id != null", "quote_id": "0Q0aj000002vHcTCAU",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(Equal("Unknown price book scope"))
	g.Expect(stub.soql()).To(BeEmpty())
}

// Salesforce sets a quote's or order's price book FROM its first line item, so a
// parent that has not got one yet is a legitimate state and every active entry is
// a legal choice. Refusing to load would strand the operator on the first line of
// every new quote.
func TestSalesforcePriceBookEntriesWidenWhenTheParentHasNoBookYet(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe,
		`{"records":[{"Pricebook2Id":null}]}`,
		sfTwoEntriesOneBook)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"scope": "order", "order_id": "801aj00003DvQhVAAV",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())
	g.Expect(stub.soql()[1]).ToNot(ContainSubstring("Pricebook2Id ="))
	// And with no single book pinned, every row says which book it is from.
	g.Expect(sfOptionNames(body)).To(ContainElement(HavePrefix("Standard: ")))
}

// A parent the org no longer has is the operator's problem to fix, not a spinning
// dropdown.
func TestSalesforcePriceBookEntriesExplainAVanishedParent(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, `{"records":[]}`)

	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"scope": "quote", "quote_id": "0Q0aj000002vHcTCAU",
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("isn't in Salesforce any more"))
	// Refused after the parent hop and before the entry query.
	g.Expect(stub.soql()).To(HaveLen(1))
}

// The product is an OPTIONAL narrowing on the line-item actions. An operator who
// has not filled it in yet — or whose binding has not resolved — must still get a
// list, because blanking the picker over an optional filter is the worse failure.
func TestSalesforcePriceBookEntriesNarrowByProductAndIgnoreAnUnresolvedOne(t *testing.T) {
	g := NewWithT(t)

	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)
	r := setupSalesforceRouter(&Service{})
	body, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"product_id": "01taj00000UI9YDAA1",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(ContainSubstring("Product2Id = '01taj00000UI9YDAA1'"))
	// Narrowing must still RETURN the prices, not just ask the right question.
	g.Expect(sfError(body)).To(BeEmpty())
	g.Expect(sfOptionValues(body)).To(HaveLen(2))

	stub = sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)
	r = setupSalesforceRouter(&Service{})
	body, code = getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"product_id": "${node.abc.output.id}",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("Product2Id"))
	g.Expect(stub.soql()[0]).ToNot(ContainSubstring("${"))
	g.Expect(sfOptionValues(body)).To(HaveLen(2))
}

// The search box is the only free-text value in this statement, and it goes into a
// LIKE across a RELATIONSHIP (Product2.Name — the words the operator is typing).
// SOQL has no bind variables, so the escaping is asserted on the query actually
// sent.
func TestSalesforcePriceBookEntriesSearchCannotEscapeTheLikeLiteral(t *testing.T) {
	g := NewWithT(t)
	stub := sfPriceBookEntryStub(t, sfPricebookEntryDescribe, sfTwoEntriesOneBook)

	r := setupSalesforceRouter(&Service{})
	_, code := getSalesforceOptions(r, "salesforce-price-book-entries", sfAuth(map[string]string{
		"search": `50% off' OR Id != null OR Name LIKE '`,
	}))

	g.Expect(code).To(Equal(http.StatusOK))
	soql := stub.soql()[0]
	g.Expect(soql).To(ContainSubstring("Product2.Name LIKE '"))
	// Exactly one literal survives: two unescaped quotes, no more. The crafted OR
	// is still in the string — it just cannot get OUT of the literal, which is the
	// property that matters.
	g.Expect(sfUnescapedQuotes(soql)).To(Equal(2))
	g.Expect(soql).To(ContainSubstring(`\' OR Id != null`))
	// And the wildcard in "50%" stays literal rather than matching everything.
	g.Expect(soql).To(ContainSubstring(`50\%`))
}

// ---------------------------------------------------------------------------
// 11d. Contract statuses, by what the status actually does
// ---------------------------------------------------------------------------

// Contract.Status is a restricted picklist of three in a stock org, and two of
// this node's four contract actions accept exactly ONE of them:
//
//   - Activate Contract: PATCHing Status "Draft" or "In Approval Process" onto a
//     draft contract answers 204 and leaves ActivatedDate null (verified live), so
//     the action reports "Activated contract …" over a contract that is still a
//     draft and every downstream step takes the success branch.
//   - Create Contract: POSTing anything but the draft status is 400
//     FAILED_ACTIVATION every single time (verified live).
//
// Which status does which is not in the field describe — it is the StatusCode on
// the ContractStatus object — so these two inputs get a picker built from that,
// the same way Convert Lead's does.

const sfContractStatusDescribe = `{"fields":[
  {"name":"Id","type":"id"},
  {"name":"ContractNumber","label":"Contract Number","type":"string","nameField":true,"filterable":true,"sortable":true},
  {"name":"Status","label":"Status","type":"picklist","createable":true,"updateable":true,
   "filterable":true,"sortable":true,
   "picklistValues":[
     {"active":true,"label":"In Approval Process","value":"In Approval Process"},
     {"active":true,"label":"Activated","value":"Activated"},
     {"active":true,"label":"Draft","value":"Draft"}
   ]}
]}`

// sfContractStatusStub answers the Contract describe and every /query with one
// canned body each.
func sfContractStatusStub(t *testing.T, describe, query string) *salesforceStub {
	t.Helper()
	stub := newSalesforceStub(t)
	stub.handler = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/describe") {
			_, _ = w.Write([]byte(describe))
			return true
		}
		_, _ = w.Write([]byte(query))
		return true
	}
	return stub
}

func TestSalesforceActivateContractOnlyOffersAStatusThatActivates(t *testing.T) {
	g := NewWithT(t)
	// A real org answers the WHERE; the stub returns the activating row, so the
	// assertion below is about the STATEMENT, which is what does the filtering.
	stub := sfContractStatusStub(t, sfContractStatusDescribe,
		`{"records":[{"MasterLabel":"Activated","ApiName":"Activated","IsDefault":false}]}`)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Activated",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(BeEmpty())
	g.Expect(stub.soql()).To(HaveLen(1))
	g.Expect(stub.soql()[0]).To(Equal(
		"SELECT MasterLabel, ApiName, IsDefault FROM ContractStatus WHERE StatusCode = 'Activated' ORDER BY SortOrder LIMIT 200"))
	// Contract.Status takes the picklist VALUE, and only the activating one is here —
	// the other two rows of the same picklist are what the old marker offered.
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Activated"}))
	g.Expect(sfOptionValues(body)).ToNot(ContainElement("Draft"))
	g.Expect(sfOptionValues(body)).ToNot(ContainElement("In Approval Process"))

	// The marker has to point here and not at the picklist, or this endpoint is dead
	// code and the dropdown on a step called "Activate Contract" still offers Draft.
	marker, registered := dynamicOptionsMetadata["crm/salesforce/contract_activate#contract_status"]
	g.Expect(registered).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/salesforce-contract-statuses?status_code=Activated"))

	// And the two actions that legitimately name ANY status keep the full picklist:
	// moving a contract to In Approval Process and reporting on drafts are both real
	// things to do, so narrowing those would be the opposite defect.
	for _, action := range []string{"contract_update", "contract_get_all"} {
		marker, registered = dynamicOptionsMetadata["crm/salesforce/"+action+"#contract_status"]
		g.Expect(registered).To(BeTrue(), action)
		g.Expect(marker.Endpoint).To(Equal(
			"/api/v1/action/options/salesforce-picklist?object=Contract&field=Status"), action)
	}
}

func TestSalesforceCreateContractOnlyOffersTheStatusItCanBeCreatedIn(t *testing.T) {
	g := NewWithT(t)
	stub := sfContractStatusStub(t, sfContractStatusDescribe,
		`{"records":[{"MasterLabel":"Draft","ApiName":"Draft","IsDefault":true}]}`)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Draft",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(stub.soql()[0]).To(ContainSubstring("WHERE StatusCode = 'Draft'"))
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Draft"}))
	// The status a new contract lands in unless it is told otherwise, so saying so
	// is the difference between one row and one row that explains itself.
	g.Expect(sfOptionNames(body)).To(Equal([]string{"Draft (default)"}))

	marker, registered := dynamicOptionsMetadata["crm/salesforce/contract_create#contract_status"]
	g.Expect(registered).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/salesforce-contract-statuses?status_code=Draft"))
}

// The case both inputs exist for: an administrator has RENAMED the live status.
// The org then has a MasterLabel that is not the string the restricted picklist
// accepts, and committing the label would be INVALID_OR_NULL_FOR_RESTRICTED_PICKLIST.
// So the row is CHOSEN from ContractStatus and its value comes from the describe.
func TestSalesforceContractStatusesSendTheValueNotTheRenamedLabel(t *testing.T) {
	g := NewWithT(t)
	sfContractStatusStub(t, `{"fields":[
	  {"name":"Status","label":"Status","type":"picklist",
	   "picklistValues":[
	     {"active":true,"label":"Live","value":"Activated"},
	     {"active":true,"label":"Draft","value":"Draft"}
	   ]}
	]}`, `{"records":[{"MasterLabel":"Live","ApiName":"Activated","IsDefault":false}]}`)
	r := setupSalesforceRouter(&Service{})

	body, _ := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Activated",
	}))
	g.Expect(sfOptionNames(body)).To(Equal([]string{"Live"}), "the operator reads their org's own wording")
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Activated"}), "the flow sends what the field accepts")
}

// A status retired in Setup is still a ContractStatus row, but it is no longer an
// active picklist value and writing it is refused — the same rule as the retired
// picklist value and the deactivated user. The describe is what decides.
func TestSalesforceContractStatusesDropAStatusRetiredInSetup(t *testing.T) {
	g := NewWithT(t)
	sfContractStatusStub(t, sfContractStatusDescribe, `{"records":[
	  {"MasterLabel":"Retired Activated","ApiName":"Retired Activated","IsDefault":false},
	  {"MasterLabel":"Activated","ApiName":"Activated","IsDefault":false}
	]}`)
	r := setupSalesforceRouter(&Service{})

	body, _ := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Activated",
	}))
	g.Expect(sfOptionValues(body)).To(Equal([]string{"Activated"}))
}

// An org whose Contract Status list has nothing in the category gets told what to
// change and where, not an empty box.
func TestSalesforceContractStatusesExplainAnEmptyCategory(t *testing.T) {
	g := NewWithT(t)
	sfContractStatusStub(t, sfContractStatusDescribe, `{"records":[]}`)
	r := setupSalesforceRouter(&Service{})

	body, code := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Activated",
	}))
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(sfError(body)).To(ContainSubstring("Setup ▸ Contract Status"))
	g.Expect(sfError(body)).To(ContainSubstring("activates a contract"))

	// The other category says its own thing rather than the wrong org's fix.
	body, _ = getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
		"status_code": "Draft",
	}))
	g.Expect(sfError(body)).To(ContainSubstring("start a new contract"))
}

// ?status_code= is OUR parameter, baked into the marker endpoint. Anything outside
// the closed set is a bug or a hand-crafted request, and either way it must not
// reach the org — a category nobody validated is how a picker starts offering
// statuses the action cannot use again.
func TestSalesforceContractStatusesRefuseAnUnknownCategory(t *testing.T) {
	g := NewWithT(t)
	stub := sfContractStatusStub(t, sfContractStatusDescribe, `{"records":[]}`)
	r := setupSalesforceRouter(&Service{})

	for _, bad := range []string{"", "Terminated", "activated", "Activated' OR StatusCode != null--"} {
		stub.reset()
		body, code := getSalesforceOptions(r, "salesforce-contract-statuses", sfAuth(map[string]string{
			"status_code": bad,
		}))
		g.Expect(code).To(Equal(http.StatusOK), bad)
		g.Expect(sfError(body)).ToNot(BeEmpty(), bad)
		g.Expect(stub.soql()).To(BeEmpty(), "%q reached the org", bad)
	}
}

// ---------------------------------------------------------------------------
// 12. Marker integrity
// ---------------------------------------------------------------------------

// salesforceMarkers returns every registered Salesforce marker.
func salesforceMarkers() map[string]api.InputDynamicOptions {
	out := map[string]api.InputDynamicOptions{}
	for key, marker := range dynamicOptionsMetadata {
		if strings.HasPrefix(key, "crm/salesforce/") {
			out[key] = marker
		}
	}
	return out
}

// salesforceMarkerQuery splits a marker endpoint into its slug and baked query.
func salesforceMarkerQuery(t *testing.T, endpoint string) (string, url.Values) {
	t.Helper()
	slug, raw, _ := strings.Cut(strings.TrimPrefix(endpoint, "/api/v1/action/options/"), "?")
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("marker endpoint %q has an unparseable query: %v", endpoint, err)
	}
	return slug, values
}

// salesforceBakedObjects is every object name baked into a marker endpoint,
// including the members of a polymorphic list.
func salesforceBakedObjects(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	out := []string{}
	for _, marker := range salesforceMarkers() {
		_, values := salesforceMarkerQuery(t, marker.Endpoint)
		for _, part := range strings.Split(values.Get("object"), ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// Dropping environment is not cosmetic: the token usually arrives as a
// ${secrets.X} or ${credentials.X} reference, and without the environment id the
// api cannot resolve it — the picker then dies for every operator who is not
// pasting a raw token, which is nearly all of them.
func TestSalesforceMarkersCarryTheFullAuthTrio(t *testing.T) {
	g := NewWithT(t)

	markers := salesforceMarkers()
	g.Expect(markers).To(HaveLen(565), "marker count changed — check the table in salesforce_options_markers.go")

	for key, marker := range markers {
		g.Expect(marker.Endpoint).To(HavePrefix("/api/v1/action/options/salesforce-"), key)
		for _, required := range []string{"access_token", "instance_url", "environment"} {
			g.Expect(marker.Params).To(ContainElement(required), key)
		}
	}
}

// A marker pointing at a route the service does not serve is a silent 404 in the
// editor and a dropdown that never loads. This checks the REAL router, not a
// hand-maintained list.
func TestSalesforceMarkerEndpointsAreRegisteredRoutes(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	s := &Service{engine: gin.New()}
	g.Expect(func() { s.registerRoutes(&config.Config{}) }).ToNot(Panic())
	routes := s.engine.Routes()

	for key, marker := range salesforceMarkers() {
		slug, _ := salesforceMarkerQuery(t, marker.Endpoint)
		path := "/api/v1/action/options/" + slug
		g.Expect(countRoutes(routes, http.MethodGet, path)).
			To(Equal(1), "%s points at %q, which the service does not serve exactly once", key, path)
	}

	// And every slug this test file exercises is a real route too, so the test
	// router above cannot drift away from the service.
	for _, slug := range salesforceProxySlugs {
		g.Expect(countRoutes(routes, http.MethodGet, "/api/v1/action/options/"+slug)).To(Equal(1), slug)
	}
}

// A baked identifier that the handler's own whitelist rejects is a picker that
// can only ever error.
func TestSalesforceMarkersBakeUsableParameters(t *testing.T) {
	g := NewWithT(t)

	needsObject := map[string]bool{
		"salesforce-fields": true, "salesforce-picklist": true,
		"salesforce-external-id-fields": true, "salesforce-record-types": true,
		"salesforce-lookup": true, "salesforce-owners": true, "salesforce-list-views": true,
	}

	for key, marker := range salesforceMarkers() {
		slug, values := salesforceMarkerQuery(t, marker.Endpoint)

		// Baked object names must satisfy the same whitelist the handler applies,
		// and a polymorphic list must fit under the fan-out cap or the tail is
		// silently truncated.
		if raw := values.Get("object"); raw != "" {
			parts := strings.Split(raw, ",")
			g.Expect(len(parts)).To(BeNumerically("<=", salesforceMaxLookupObjects),
				"%s bakes %d objects — anything past %d is silently dropped", key, len(parts), salesforceMaxLookupObjects)
			for _, part := range parts {
				g.Expect(salesforceObjectPattern.MatchString(strings.TrimSpace(part))).
					To(BeTrue(), "%s bakes an object the handler will refuse: %q", key, part)
			}
			if slug != "salesforce-lookup" {
				g.Expect(parts).To(HaveLen(1), "%s: only the lookup picker accepts an object list", key)
			}
		}

		// Baked field names likewise.
		if field := values.Get("field"); field != "" {
			g.Expect(salesforceFieldPattern.MatchString(field)).
				To(BeTrue(), "%s bakes a field the handler will refuse: %q", key, field)
		}

		// The filter vocabulary is a closed set validated before describe.
		if filter := values.Get("filter"); filter != "" {
			_, known := salesforceFieldFilters[filter]
			g.Expect(known).To(BeTrue(), "%s bakes an unknown field filter %q", key, filter)
		}

		// And the filter has to MATCH what the input does with the field. A Sort By
		// filled from every field offers rows Salesforce answers with
		// MALFORMED_QUERY; a Filter Field / Look Up By likewise with INVALID_FIELD.
		// The SELECT-list inputs are the ones that legitimately take everything.
		if slug == "salesforce-fields" {
			input := key[strings.LastIndex(key, "#")+1:]
			switch input {
			case "order_by":
				g.Expect(values.Get("filter")).To(Equal("sortable"),
					"%s: a Sort By list must only offer fields Salesforce can sort on", key)
			case "filter_field", "match_field":
				g.Expect(values.Get("filter")).To(Equal("filterable"),
					"%s: a filter list must only offer fields Salesforce can filter on", key)
			}
		}

		// The picklist picker needs BOTH halves of the (object, field) pair, and
		// neither is ever forwarded — they ride in the endpoint.
		if slug == "salesforce-picklist" {
			g.Expect(values.Get("object")).ToNot(BeEmpty(), "%s: picklist marker with no object", key)
			g.Expect(values.Get("field")).ToNot(BeEmpty(), "%s: picklist marker with no field", key)
		}

		// The two-hop picker is useless without the campaign the operator chose.
		if slug == "salesforce-campaign-member-status" {
			g.Expect(marker.Params).To(ContainElement("campaign_id"), key)
		}

		// The contract status picker's category is a closed set too, and a marker
		// that bakes none of it can only ever answer "Unknown contract status
		// category" — a dropdown that never loads however the panel is filled in.
		if slug == "salesforce-contract-statuses" {
			code := values.Get("status_code")
			_, known := salesforceContractStatusCodes[code]
			g.Expect(known).To(BeTrue(), "%s bakes an unknown contract status category %q", key, code)
		}

		// The price book entry picker's scope is a closed set, and a scoped marker
		// that does NOT forward its parent's id can only ever answer "choose the
		// quote first" — a dropdown that never loads however the operator fills the
		// panel in.
		if slug == "salesforce-price-book-entries" {
			scopeName := values.Get("scope")
			if scopeName != "" {
				scope, known := salesforcePriceBookEntryScopes[scopeName]
				g.Expect(known).To(BeTrue(), "%s bakes an unknown price book scope %q", key, scopeName)
				g.Expect(marker.Params).To(ContainElement(scope.Param),
					"%s scopes by %s but never forwards %s", key, scopeName, scope.Param)
			}
		}

		// Every object-scoped picker either bakes an object or forwards the
		// action's own object input — otherwise it can only error.
		if needsObject[slug] && values.Get("object") == "" {
			forwarded := false
			for _, p := range marker.Params {
				if p == "object" || p == "custom_object" || p == "link_to_object" {
					forwarded = true
				}
			}
			g.Expect(forwarded).To(BeTrue(), "%s neither bakes nor forwards an object", key)
		}

		// A forwarded object parameter must be one salesforceQueryObject reads.
		for _, p := range marker.Params {
			switch p {
			case "access_token", "instance_url", "environment",
				"object", "custom_object", "link_to_object", "campaign_id",
				// The two-hop price book entry picker's parent scopes, plus the
				// optional product narrowing.
				"quote_id", "order_id", "opportunity_id", "product_id":
			default:
				t.Errorf("%s forwards %q, which no Salesforce proxy reads", key, p)
			}
		}
	}
}

// Two groups claiming the same (action, input) would silently leave whichever
// registered last — exactly the drift that makes a picker point at the wrong
// object. init() panics on a duplicate; this pins the invariant it protects.
func TestSalesforceMarkerKeysAreUniquePerActionInput(t *testing.T) {
	g := NewWithT(t)

	seen := map[string]string{}
	for _, group := range salesforcePickerGroups {
		for _, action := range group.Actions {
			key := action + "#" + group.Input
			previous, dup := seen[key]
			g.Expect(dup).To(BeFalse(), "%s registered twice: %q then %q", key, previous, group.Endpoint)
			seen[key] = group.Endpoint
		}
	}
	g.Expect(seen).To(HaveLen(len(salesforceMarkers())))
}

// The auto-fill leaves the Instance URL box BLANK in the editor — the value is
// only synthesised at run time, in the executor. So the picker request arrives
// with instance_url empty, and all 566 dropdowns depend on the proxy deriving it
// from the credential instead. A broken dropdown looks like an org with no
// records in it, not like a bug, so this is the failure that would not announce
// itself.
//
// These call resolveSalesforceConn DIRECTLY and clear salesforceOptionsHostOverride
// first. Going through getSalesforceOptions would have proved nothing: the stub
// sets that override, and line 411 replaces `base` with it regardless of what the
// instance URL resolved to — so the assertions passed even with the fallback
// deleted. Verified by mutation; the earlier version of these tests was worthless.
func sfConnForTest(t *testing.T, mock *sfMockPersistence, params map[string]string) (salesforceProxyConn, string, bool) {
	t.Helper()
	prev := salesforceOptionsHostOverride
	salesforceOptionsHostOverride = "" // real validation, no test seam
	t.Cleanup(func() { salesforceOptionsHostOverride = prev })

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/options?"+q.Encode(), nil)
	c.Set("account_id", "user-1") // same injection setupSalesforceRouter uses

	s := &Service{persistence: mock}
	return s.resolveSalesforceConn(c)
}

func TestSalesforceBlankInstanceURLFallsBackToTheCredential(t *testing.T) {
	g := NewWithT(t)
	mock := newSFMockPersistence()
	mock.credName = "SALESFORCE_PROD"
	mock.credToken = "managed-access-token"
	mock.credMeta = `{"instance_url":"https://acme.my.salesforce.com"}`

	conn, msg, ok := sfConnForTest(t, mock, map[string]string{
		"access_token": "${credentials.SALESFORCE_PROD}",
		"instance_url": "", // left blank by the operator — the whole point
		"environment":  sfEnvID,
	})

	g.Expect(msg).To(BeEmpty(), "a blank Instance URL with a connected credential must resolve, not error")
	g.Expect(ok).To(BeTrue())
	g.Expect(conn.BaseURL).To(Equal("https://acme.my.salesforce.com"),
		"the host must come from the credential's captured instance_url")
	g.Expect(conn.Token).To(Equal("managed-access-token"))
}

// The parameter absent entirely, rather than present-and-empty.
func TestSalesforceAbsentInstanceURLFallsBackToTheCredential(t *testing.T) {
	g := NewWithT(t)
	mock := newSFMockPersistence()
	mock.credName = "SALESFORCE_PROD"
	mock.credToken = "managed-access-token"
	mock.credMeta = `{"instance_url":"https://acme.my.salesforce.com"}`

	conn, msg, ok := sfConnForTest(t, mock, map[string]string{
		"access_token": "${credentials.SALESFORCE_PROD}",
		"environment":  sfEnvID,
	})

	g.Expect(msg).To(BeEmpty())
	g.Expect(ok).To(BeTrue())
	g.Expect(conn.BaseURL).To(Equal("https://acme.my.salesforce.com"))
}

// The bring-your-own-token path has no credential to derive from, so a blank
// Instance URL must still be refused with a message rather than a dead dropdown.
// Auto-fill must not weaken this.
func TestSalesforceBlankInstanceURLWithAPastedTokenIsStillRefused(t *testing.T) {
	g := NewWithT(t)

	_, msg, ok := sfConnForTest(t, newSFMockPersistence(), map[string]string{
		"access_token": "raw-pasted-token",
		"instance_url": "",
		"environment":  sfEnvID,
	})

	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("Instance URL"),
		"with nothing to derive from, the operator must be told which field to fill in")
}
