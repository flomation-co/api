package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	api "flomation.app/automate/api"
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
		ginCtxForTest(),
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

// ginCtxForTest builds a throwaway gin context; captureProviderTenant only reads
// query params from it, and the salesforce path reads none.
func ginCtxForTest() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/callback", nil)
	return c
}
