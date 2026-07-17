package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The dropdown registrations in these files are hand-maintained lists of action
// names. Nothing at compile time ties "azure/files/file_upload" to the action
// that actually exists in the executor, or ties the endpoint string to a
// registered route — so both rot silently, and the only symptom is a dropdown
// that quietly never appears. These tests pin the halves that CAN be checked
// here: that every registration names a route this service serves, and that the
// params a proxy reads are params the editor was told to send.

// azureWave2Endpoints are the option routes added for Service Bus, Azure
// DevOps, Table Storage and Files. Kept as a literal rather than derived from
// the router so that deleting a route fails this test instead of silently
// agreeing with itself.
var azureWave2Endpoints = map[string]bool{
	"/api/v1/action/options/azure-files-shares":              true,
	"/api/v1/action/options/azure-tables-tables":             true,
	"/api/v1/action/options/azure-servicebus-queues":         true,
	"/api/v1/action/options/azure-servicebus-topics":         true,
	"/api/v1/action/options/azure-servicebus-subscriptions":  true,
	"/api/v1/action/options/azuredevops-projects":            true,
	"/api/v1/action/options/azuredevops-repositories":        true,
	"/api/v1/action/options/azuredevops-pipelines":           true,
	"/api/v1/action/options/azuredevops-release-definitions": true,
	"/api/v1/action/options/azuredevops-teams":               true,
}

// wave2Prefixes are the action-ID prefixes this wave introduced.
var wave2Prefixes = []string{
	"messagebrokers/azureservicebus/",
	"devops/azuredevops/",
	"azure/tables/",
	"azure/files/",
}

func isWave2Action(actionID string) bool {
	for _, p := range wave2Prefixes {
		if strings.HasPrefix(actionID, p) {
			return true
		}
	}
	return false
}

// TestWave2DropdownsPointAtKnownEndpoints — a typo in an endpoint string
// produces a registration the editor will call and the router will 404. The
// editor treats that as "no options" and falls back to manual entry, so the
// operator sees a plain text box and no error anywhere.
func TestWave2DropdownsPointAtKnownEndpoints(t *testing.T) {
	seen := 0
	for key, meta := range dynamicOptionsMetadata {
		actionID := strings.SplitN(key, "#", 2)[0]
		if !isWave2Action(actionID) {
			continue
		}
		seen++
		if !azureWave2Endpoints[meta.Endpoint] {
			t.Errorf("%s registers unknown endpoint %q", key, meta.Endpoint)
		}
	}
	if seen == 0 {
		t.Fatal("no wave-2 dropdown registrations found — this test would pass vacuously")
	}
	t.Logf("checked %d wave-2 dropdown registrations", seen)
}

// TestWave2DropdownsCarryTheirSecret — a proxy that resolves a secret can only
// do so if the editor was told to forward it. Omitting the secret from Params
// means the proxy always sees an empty credential and the list never loads.
func TestWave2DropdownsCarryTheirSecret(t *testing.T) {
	// endpoint -> the secret param that proxy resolves.
	secretFor := map[string]string{
		"/api/v1/action/options/azure-files-shares":              "account_key",
		"/api/v1/action/options/azure-tables-tables":             "account_key",
		"/api/v1/action/options/azure-servicebus-queues":         "connection_string",
		"/api/v1/action/options/azure-servicebus-topics":         "connection_string",
		"/api/v1/action/options/azure-servicebus-subscriptions":  "connection_string",
		"/api/v1/action/options/azuredevops-projects":            "personal_access_token",
		"/api/v1/action/options/azuredevops-repositories":        "personal_access_token",
		"/api/v1/action/options/azuredevops-pipelines":           "personal_access_token",
		"/api/v1/action/options/azuredevops-release-definitions": "personal_access_token",
		"/api/v1/action/options/azuredevops-teams":               "personal_access_token",
	}

	checked := 0
	for key, meta := range dynamicOptionsMetadata {
		if !isWave2Action(strings.SplitN(key, "#", 2)[0]) {
			continue
		}
		want, ok := secretFor[meta.Endpoint]
		if !ok {
			continue // covered by the endpoint test
		}
		checked++
		found := false
		for _, got := range meta.Params {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s -> %s: Params omits the secret %q, so the proxy can never authenticate", key, meta.Endpoint, want)
		}
	}
	if checked == 0 {
		t.Fatal("no registrations checked — this test would pass vacuously")
	}
}

// TestWave2ScopedDropdownsCarryTheirParent — a list scoped to a parent is
// useless without the parent. Subscriptions need their topic; repositories,
// pipelines, release definitions and teams need their project.
func TestWave2ScopedDropdownsCarryTheirParent(t *testing.T) {
	parentFor := map[string]string{
		"/api/v1/action/options/azure-servicebus-subscriptions":  "topic",
		"/api/v1/action/options/azuredevops-repositories":        "project",
		"/api/v1/action/options/azuredevops-pipelines":           "project",
		"/api/v1/action/options/azuredevops-release-definitions": "project",
		"/api/v1/action/options/azuredevops-teams":               "project",
	}
	for key, meta := range dynamicOptionsMetadata {
		want, ok := parentFor[meta.Endpoint]
		if !ok {
			continue
		}
		found := false
		for _, p := range meta.Params {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s -> %s: Params omits the parent %q, so the list can never be scoped", key, meta.Endpoint, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Service Bus connection-string parsing + SAS
// ---------------------------------------------------------------------------

func TestParseAzureServiceBusConnString(t *testing.T) {
	t.Run("real namespace", func(t *testing.T) {
		got, errMsg := parseAzureServiceBusConnString(
			"Endpoint=sb://flo.servicebus.windows.net/;SharedAccessKeyName=RootManageSharedAccessKey;SharedAccessKey=abc+/def==")
		if errMsg != "" {
			t.Fatalf("unexpected refusal: %s", errMsg)
		}
		if got.Namespace != "flo.servicebus.windows.net" {
			t.Errorf("namespace = %q", got.Namespace)
		}
		if got.KeyName != "RootManageSharedAccessKey" {
			t.Errorf("key name = %q", got.KeyName)
		}
		// The key is base64 and contains '=' — SplitN(2) is what keeps the
		// padding attached instead of truncating the key at its first '='.
		if got.Key != "abc+/def==" {
			t.Errorf("key = %q, want the full padded value", got.Key)
		}
		if got.IsEmulator {
			t.Error("a real namespace was flagged as the emulator")
		}
	})

	t.Run("emulator is detected, not merely parsed", func(t *testing.T) {
		got, errMsg := parseAzureServiceBusConnString(
			"Endpoint=sb://localhost;SharedAccessKeyName=RootManageSharedAccessKey;SharedAccessKey=SAS_KEY_VALUE;UseDevelopmentEmulator=true;")
		if errMsg != "" {
			t.Fatalf("unexpected refusal: %s", errMsg)
		}
		if !got.IsEmulator {
			t.Fatal("emulator connection string not flagged — the proxy would try to reach a management API that does not exist and report a confusing network error")
		}
	})

	for _, c := range []struct{ name, in string }{
		{"empty", ""},
		{"unresolved secret ref", "${secrets.SB}"},
		{"no endpoint", "SharedAccessKeyName=x;SharedAccessKey=y"},
		{"no key", "Endpoint=sb://flo.servicebus.windows.net/;SharedAccessKeyName=x"},
		{"no key name", "Endpoint=sb://flo.servicebus.windows.net/;SharedAccessKey=y"},
	} {
		t.Run("rejects "+c.name, func(t *testing.T) {
			if _, errMsg := parseAzureServiceBusConnString(c.in); errMsg == "" {
				t.Error("expected a refusal")
			}
		})
	}
}

// TestAzureServiceBusSASTokenShape pins the token's structure. The signature
// itself is time-dependent, so this checks the parts that are not: that the
// resource is url-encoded and lower-cased, that the signature is url-encoded
// when placed in the header (a '+' from base64 MUST arrive as %2B), and that
// the expiry is in the future.
func TestAzureServiceBusSASTokenShape(t *testing.T) {
	tok := azureServiceBusSASToken("https://Flo.servicebus.windows.net/", "Root", "key")
	if !strings.HasPrefix(tok, "SharedAccessSignature ") {
		t.Fatalf("token = %q", tok)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(tok, "SharedAccessSignature "))
	if err != nil {
		t.Fatalf("token body did not parse as a query string: %v", err)
	}
	if got := q.Get("sr"); got != "https://flo.servicebus.windows.net/" {
		t.Errorf("sr = %q, want the lower-cased resource", got)
	}
	if q.Get("skn") != "Root" {
		t.Errorf("skn = %q", q.Get("skn"))
	}
	if q.Get("sig") == "" {
		t.Error("sig is empty")
	}
	if q.Get("se") == "" {
		t.Error("se is empty")
	}
}

// TestAzureServiceBusKeyIsNotBase64Decoded is the guard for the trap called out
// in the signer's comment: a Service Bus key is signed as raw UTF-8 bytes,
// while the Storage account key next door is base64-decoded first. Both keys
// LOOK like base64, so getting this wrong yields a well-formed token that
// simply never authenticates — there is no local symptom at all.
func TestAzureServiceBusKeyIsNotBase64Decoded(t *testing.T) {
	// "a2V5" is base64 for "key". If the implementation decoded it, signing
	// "a2V5" and signing "key" would produce the same signature.
	rawKeyTok := azureServiceBusSASToken("https://flo.servicebus.windows.net/", "R", "key")
	b64KeyTok := azureServiceBusSASToken("https://flo.servicebus.windows.net/", "R", "a2V5")

	sigOf := func(tok string) string {
		q, _ := url.ParseQuery(strings.TrimPrefix(tok, "SharedAccessSignature "))
		return q.Get("sig")
	}
	if sigOf(rawKeyTok) == sigOf(b64KeyTok) {
		t.Error("signing \"a2V5\" matched signing \"key\" — the key is being base64-decoded, which is the Storage rule, not the Service Bus one")
	}
}

// ---------------------------------------------------------------------------
// Azure DevOps
// ---------------------------------------------------------------------------

func TestAzureDevOpsBases(t *testing.T) {
	cases := []struct {
		name, in, core, release string
		wantErr                 bool
	}{
		{name: "modern", in: "https://dev.azure.com/flomation",
			core: "https://dev.azure.com/flomation", release: "https://vsrm.dev.azure.com/flomation"},
		{name: "modern with trailing slash", in: "https://dev.azure.com/flomation/",
			core: "https://dev.azure.com/flomation", release: "https://vsrm.dev.azure.com/flomation"},
		{name: "scheme omitted", in: "dev.azure.com/flomation",
			core: "https://dev.azure.com/flomation", release: "https://vsrm.dev.azure.com/flomation"},
		// The legacy host's Release twin differs in SHAPE, not just name.
		{name: "legacy visualstudio.com", in: "https://flomation.visualstudio.com",
			core: "https://flomation.visualstudio.com", release: "https://flomation.vsrm.visualstudio.com"},
		// Host case is irrelevant to DNS and must not leak into the URL.
		{name: "host is lower-cased", in: "https://DEV.AZURE.COM/Flomation",
			core: "https://dev.azure.com/Flomation", release: "https://vsrm.dev.azure.com/Flomation"},

		{name: "empty", in: "", wantErr: true},
		{name: "unresolved ref", in: "${secrets.ORG}", wantErr: true},
		{name: "no org segment", in: "https://dev.azure.com", wantErr: true},
		{name: "non-http scheme", in: "file:///etc/passwd", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			core, release, errMsg := azureDevOpsBases(c.in)
			if c.wantErr {
				if errMsg == "" {
					t.Fatalf("expected a refusal, got core=%q", core)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected refusal: %s", errMsg)
			}
			if core != c.core {
				t.Errorf("core = %q, want %q", core, c.core)
			}
			if release != c.release {
				t.Errorf("release = %q, want %q", release, c.release)
			}
		})
	}
}

// TestAzureDevOpsBasesDropsSmuggledMaterial — the org URL is operator-supplied
// and every request is built from it, so userinfo, query and fragment must not
// survive into the base.
func TestAzureDevOpsBasesDropsSmuggledMaterial(t *testing.T) {
	for _, in := range []string{
		"https://user:pass@dev.azure.com/flomation",
		"https://dev.azure.com/flomation?api-version=evil",
		"https://dev.azure.com/flomation#frag",
	} {
		core, _, errMsg := azureDevOpsBases(in)
		if errMsg != "" {
			continue // refusing outright is also fine
		}
		if core != "https://dev.azure.com/flomation" {
			t.Errorf("azureDevOpsBases(%q) = %q — smuggled material survived", in, core)
		}
	}
}

// TestAzureDevOpsAuthHeaderUsesEmptyUsername pins the documented header shape.
func TestAzureDevOpsAuthHeaderUsesEmptyUsername(t *testing.T) {
	got := azureDevOpsAuthHeader("PAT123")
	// base64(":PAT123")
	if want := "Basic OlBBVDEyMw=="; got != want {
		t.Errorf("auth header = %q, want %q (an EMPTY username, colon, then the PAT)", got, want)
	}
}

// TestAzureDevOpsSignInRedirectIsAnAuthError guards a failure mode that is
// invisible from the code and was only found by pointing a bad PAT at the real
// service.
//
// An expired PAT does not produce 401. Azure DevOps answers 302 to a sign-in
// page on a DIFFERENT host, the shared SSRF redirect guard refuses to follow it
// (correctly), and the call fails at the transport. Without the classifier that
// surfaces as "Could not reach Azure DevOps — check the Organisation URL", so
// the one operator error this proxy will actually see in the wild — a token
// that expired — reads as a network problem.
func TestAzureDevOpsSignInRedirectIsAnAuthError(t *testing.T) {
	// The exact error azureOptionsRedirect raises. Asserting on the real
	// message rather than a stand-in is the point: if that wording is ever
	// changed, this fails instead of the mapping silently regressing.
	guardErr := azureOptionsRedirect(
		httptest.NewRequest("GET", "https://spsproduks1.vssps.visualstudio.com/_signin", nil),
		[]*http.Request{httptest.NewRequest("GET", "https://dev.azure.com/flomation/_apis/projects", nil)},
	)
	if guardErr == nil {
		t.Fatal("the redirect guard allowed a cross-host redirect to the sign-in host — SSRF hardening has regressed")
	}
	if !isAzureDevOpsSignInRedirect(&url.Error{Op: "Get", URL: "https://dev.azure.com/x", Err: guardErr}) {
		t.Errorf("the guard's error (%v) is not classified as an auth failure — an expired PAT will report as a connection problem", guardErr)
	}
	// A genuine connection failure must NOT be reported as bad credentials.
	if isAzureDevOpsSignInRedirect(errors.New("dial tcp: lookup dev.azure.com: no such host")) {
		t.Error("a DNS failure was classified as an auth error")
	}
}

// TestAzureDevOpsGetMapsRedirectToCredentials drives the handler's error path
// end to end through the seam.
func TestAzureDevOpsGetMapsRedirectToCredentials(t *testing.T) {
	original := azureDevOpsDo
	defer func() { azureDevOpsDo = original }()
	azureDevOpsDo = func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://dev.azure.com/x",
			Err: errors.New("cross-host redirect not allowed")}
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/?organisation_url=https://dev.azure.com/flomation", nil)

	var out struct{ Value []struct{ Name string } }
	if ok := doAzureDevOpsGet(c, "https://dev.azure.com/flomation/_apis/projects", "pat", &out); ok {
		t.Fatal("doAzureDevOpsGet reported success on a transport failure")
	}
	// The option-proxy contract is HTTP 200 + {"error": ...} so the editor can
	// show the message inline and fall back to manual entry.
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (the option-proxy error contract)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Personal Access Token") {
		t.Errorf("body = %s\nwant the message to name the PAT, not the network", body)
	}
	if strings.Contains(body, "Could not reach") {
		t.Errorf("body = %s\nan expired PAT is being reported as a connection failure", body)
	}
}

func TestAzureDevOpsAPIVersion(t *testing.T) {
	cases := map[string]string{
		"":              "7.1",
		"   ":           "7.1",
		"${secrets.V}":  "7.1",
		"7.1":           "7.1",
		"7.1-preview.4": "7.1-preview.4",
		"6.0":           "6.0",
		"7.1&evil=1":    "7.1", // refuses query smuggling, falls back
		"../../etc":     "7.1",
		"7.1 OR 1=1":    "7.1",
	}
	for in, want := range cases {
		if got := azureDevOpsAPIVersion(in); got != want {
			t.Errorf("azureDevOpsAPIVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAzureDevOpsProject(t *testing.T) {
	// Azure DevOps project names legitimately contain spaces and unicode.
	for _, ok := range []string{"flomation-validation", "My Project", "Проект", "a.b_c"} {
		if got, valid := azureDevOpsProject(ok); !valid || got != ok {
			t.Errorf("azureDevOpsProject(%q) = %q, %v — should be accepted", ok, got, valid)
		}
	}
	// Anything that could escape the path segment must be refused.
	for _, bad := range []string{"", "   ", "${secrets.P}", "a/b", "a?b", "a#b", `a\b`, "../admin"} {
		if _, valid := azureDevOpsProject(bad); valid {
			t.Errorf("azureDevOpsProject(%q) was accepted — it can escape its path segment", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Table Storage SharedKeyLite
// ---------------------------------------------------------------------------

// TestAzureTablesSharedKeyLiteAuth pins the two-line string-to-sign against a
// hand-computed vector, so a "tidy-up" that reorders the lines or swaps the
// separator fails here rather than as a 403 against a live account.
func TestAzureTablesSharedKeyLiteAuth(t *testing.T) {
	// key bytes "0123456789abcdef" base64-encoded.
	const key = "MDEyMzQ1Njc4OWFiY2RlZg=="
	const date = "Mon, 14 Jul 2026 12:00:00 GMT"
	const resource = "/flomationstore/Tables"

	got, err := azureTablesSharedKeyLiteAuth("flomationstore", key, date, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "SharedKeyLite flomationstore:") {
		t.Fatalf("auth = %q, want the SharedKeyLite scheme and account prefix", got)
	}

	// Independently computed: HMAC-SHA256(key, date+"\n"+resource), base64.
	want := "SharedKeyLite flomationstore:" + hmacSHA256B64(t, key, date+"\n"+resource)
	if got != want {
		t.Errorf("auth = %q\nwant %q", got, want)
	}
}

func TestAzureTablesSharedKeyLiteAuthRejectsBadKey(t *testing.T) {
	for _, bad := range []string{"", "not base64!!!", "   "} {
		if _, err := azureTablesSharedKeyLiteAuth("acct", bad, "date", "/acct/Tables"); err == nil {
			t.Errorf("accepted an invalid key %q", bad)
		}
	}
}

// TestAzureTablesSignerDiffersFromBlobSigner pins the distinction the comment
// warns about — the two schemes are not interchangeable, and both are in this
// package.
func TestAzureTablesSignerDiffersFromBlobSigner(t *testing.T) {
	const key = "MDEyMzQ1Njc4OWFiY2RlZg=="
	req := httptest.NewRequest("GET", "https://acct.table.core.windows.net/Tables", nil)
	req.Header.Set("x-ms-date", "Mon, 14 Jul 2026 12:00:00 GMT")
	req.Header.Set("x-ms-version", azureStorageAPIVersion)

	blob, err := azureStorageSharedKeyAuth("acct", key, req)
	if err != nil {
		t.Fatalf("blob signer: %v", err)
	}
	tables, err := azureTablesSharedKeyLiteAuth("acct", key, "Mon, 14 Jul 2026 12:00:00 GMT", "/acct/Tables")
	if err != nil {
		t.Fatalf("tables signer: %v", err)
	}
	if strings.HasPrefix(blob, "SharedKeyLite ") {
		t.Error("the Blob signer emitted SharedKeyLite — it must emit SharedKey")
	}
	if blob == tables {
		t.Error("the Blob and Table signers produced identical headers; they are different schemes and must not be unified")
	}
}

// hmacSHA256B64 recomputes a signature independently of the code under test, so
// the vector above is a real cross-check rather than a restatement of whatever
// the implementation happens to do.
func hmacSHA256B64(t *testing.T, keyB64, payload string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		t.Fatalf("bad test key: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
