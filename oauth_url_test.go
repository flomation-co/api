package api

import (
	"encoding/json"
	"testing"
)

func TestSubstituteURLVariables(t *testing.T) {
	// Fixed-URL provider: no placeholders, unchanged even with stray vars.
	got, err := SubstituteURLVariables("https://accounts.google.com/o/oauth2/v2/auth", nil)
	if err != nil || got != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Fatalf("fixed URL: got %q err %v", got, err)
	}

	// Per-tenant substitution.
	got, err = SubstituteURLVariables("https://{shop}.myshopify.com/admin/oauth/authorize", map[string]string{"shop": "my-store"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "https://my-store.myshopify.com/admin/oauth/authorize" {
		t.Fatalf("got %q", got)
	}

	// Missing value → error naming the placeholder.
	if _, err := SubstituteURLVariables("https://{shop}.myshopify.com/x", nil); err == nil {
		t.Fatal("expected error for unfilled placeholder")
	}

	// Host-injection attempts are rejected.
	for _, bad := range []string{"evil.com", "a/b", "a@b", "a b", "a:1", ""} {
		if _, err := SubstituteURLVariables("https://{shop}.myshopify.com/x", map[string]string{"shop": bad}); err == nil {
			t.Fatalf("expected rejection for shop=%q", bad)
		}
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	raw, err := MetadataWithURLVars(map[string]string{"shop": "flomation-dev"})
	if err != nil || raw == nil {
		t.Fatalf("build: %v", err)
	}
	got := URLVarsFromMetadata(raw)
	if got["shop"] != "flomation-dev" {
		t.Fatalf("round-trip: %v", got)
	}
	// Empty vars → nil metadata (no url_vars key stored).
	if r, _ := MetadataWithURLVars(nil); r != nil {
		t.Fatalf("expected nil metadata for empty vars, got %s", string(*r))
	}
	if got := URLVarsFromMetadata(nil); got != nil {
		t.Fatalf("nil metadata should yield nil vars, got %v", got)
	}
}

func TestProviderURLVariables(t *testing.T) {
	raw := json.RawMessage(`[{"key":"shop","label":"Shop Subdomain","placeholder":"my-store"}]`)
	p := CredentialProvider{URLVariablesRaw: &raw}
	vs := p.URLVariables()
	if len(vs) != 1 || vs[0].Key != "shop" || vs[0].Label != "Shop Subdomain" {
		t.Fatalf("parsed: %+v", vs)
	}
	// Provider with no declared variables.
	if vs := (CredentialProvider{}).URLVariables(); vs != nil {
		t.Fatalf("expected nil, got %v", vs)
	}
}
