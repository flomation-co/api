package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
)

func TestFetchXeroConnections(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"conn-1","tenantId":"ten-abc","tenantType":"ORGANISATION","tenantName":"Demo Company (UK)"}]`))
	}))
	defer srv.Close()

	old := xeroConnectionsURL
	xeroConnectionsURL = srv.URL
	defer func() { xeroConnectionsURL = old }()

	conns, err := fetchXeroConnections("access-tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer access-tok" {
		t.Errorf("expected bearer auth, got %q", gotAuth)
	}
	if len(conns) != 1 || conns[0].TenantID != "ten-abc" || conns[0].TenantName != "Demo Company (UK)" {
		t.Errorf("unexpected connections: %+v", conns)
	}
}

func TestFetchXeroConnectionsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer srv.Close()

	old := xeroConnectionsURL
	xeroConnectionsURL = srv.URL
	defer func() { xeroConnectionsURL = old }()

	if _, err := fetchXeroConnections("bad"); err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}

// tenantMockPersistence records what captureProviderTenant writes, and serves a
// credential whose metadata has MOVED ON since the caller loaded it — which is
// the whole point of the test.
type tenantMockPersistence struct {
	mockPersistence
	current *api.EnvironmentCredential // what a fresh read returns
	written *json.RawMessage           // what got saved
}

func (m *tenantMockPersistence) GetCredentialByID(id string) (*api.EnvironmentCredential, error) {
	return m.current, nil
}

func (m *tenantMockPersistence) UpdateCredentialMetadata(id string, metadata *json.RawMessage) error {
	m.written = metadata
	return nil
}

// The regression this function was rewritten for.
//
// captureProviderTenant used to merge into a snapshot the OAuth callback loaded
// at its top. Anything written to the credential in between — a cleared PKCE
// verifier, in the case that surfaced — was silently reinstated when this saved.
// The bug was invisible: the connect succeeded, the unit tests passed, and only
// reading the row back afterwards showed the spent secret still sitting there.
//
// Re-reading makes the invariant local to this function, so no caller has to know
// an ordering rule exists.
func TestCaptureProviderTenantDoesNotResurrectMetadataWrittenAfterTheCallerLoadedIt(t *testing.T) {
	// The state as it is NOW: a verifier that some earlier step already cleared.
	fresh := json.RawMessage(`{"url_vars":{"domain":"login"}}`)
	m := &tenantMockPersistence{current: &api.EnvironmentCredential{Metadata: &fresh}}
	s := &Service{persistence: m}

	s.captureProviderTenant(
		ginCtxForTest(nil),
		"cred-1", "salesforce",
		&oauthTokenResponse{AccessToken: "tok", InstanceURL: "https://x.my.salesforce.com"},
	)

	if m.written == nil {
		t.Fatal("nothing was written")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(*m.written, &got); err != nil {
		t.Fatalf("written metadata is not valid JSON: %v", err)
	}

	// The thing captureProviderTenant exists to do.
	if got["instance_url"] != "https://x.my.salesforce.com" {
		t.Errorf("instance_url was not captured: %s", *m.written)
	}
	// The regression: a key absent from the CURRENT metadata must stay absent. If
	// this function read a stale snapshot it would write the old blob back.
	if _, resurrected := got["pkce_verifier"]; resurrected {
		t.Errorf("a key cleared before this call was resurrected: %s", *m.written)
	}
	// Unrelated keys in the current state must survive the merge.
	if _, ok := got["url_vars"]; !ok {
		t.Errorf("existing metadata was dropped: %s", *m.written)
	}
}

// ginCtxForTest builds a throwaway gin context carrying the given query params.
//
// It TAKES params rather than hardcoding an empty query on purpose: the
// quickbooks branch reads realmId via c.Query, and a helper that always produced
// an empty query would hand that branch "" — sending it down the early-return
// path while the test still passed, testing nothing. The salesforce branch reads
// no params, so it passes nil.
func ginCtxForTest(params map[string]string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	target := "/callback"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c
}

// The same regression on a SECOND provider, which is the point of fixing the
// class rather than ordering one caller's write: quickbooks reaches this function
// by a different route and must be equally safe.
//
// It also exercises ginCtxForTest with a real query param. The quickbooks branch
// reads realmId via c.Query, so a helper that always produced an empty query
// would send this straight down the early-return path and the test would pass
// having asserted nothing.
func TestCaptureProviderTenantReReadsForQuickBooksToo(t *testing.T) {
	fresh := json.RawMessage(`{"url_vars":{"env":"production"}}`)
	m := &tenantMockPersistence{current: &api.EnvironmentCredential{Metadata: &fresh}}
	// The quickbooks branch also consults providerIsSandbox, which dereferences
	// s.config — a dependency the salesforce branch does not have. A bare Service
	// panics here, which is itself worth knowing about this path.
	s := &Service{persistence: m, config: &config.Config{}}

	s.captureProviderTenant(
		ginCtxForTest(map[string]string{"realmId": "1234567890"}),
		"cred-qb", "quickbooks",
		&oauthTokenResponse{AccessToken: "tok"},
	)

	if m.written == nil {
		t.Fatal("nothing was written — realmId almost certainly did not reach the handler")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(*m.written, &got); err != nil {
		t.Fatalf("written metadata is not valid JSON: %v", err)
	}
	if got["realm_id"] != "1234567890" {
		t.Errorf("realm_id was not captured from the query string: %s", *m.written)
	}
	if _, resurrected := got["pkce_verifier"]; resurrected {
		t.Errorf("a key absent from the current metadata was resurrected: %s", *m.written)
	}
	if _, ok := got["url_vars"]; !ok {
		t.Errorf("existing metadata was dropped: %s", *m.written)
	}
}
