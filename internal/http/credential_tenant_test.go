package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
