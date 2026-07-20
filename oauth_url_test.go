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

func TestProviderUsesBasicAuth(t *testing.T) {
	// Intuit and Xero require HTTP Basic auth on the token endpoint; every
	// other provider sends its client credentials in the body.
	for _, slug := range []string{"quickbooks", "xero"} {
		if !ProviderUsesBasicAuth(slug) {
			t.Errorf("expected %s to use Basic auth", slug)
		}
	}
	for _, slug := range []string{"google", "github", "shopify", "twitter", ""} {
		if ProviderUsesBasicAuth(slug) {
			t.Errorf("expected %s NOT to use Basic auth", slug)
		}
	}
}

func TestMergeMetadata(t *testing.T) {
	// nil existing → just the new keys.
	out, err := MergeMetadata(nil, map[string]interface{}{"realm_id": "123"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(*out, &m); err != nil {
		t.Fatal(err)
	}
	if m["realm_id"] != "123" {
		t.Errorf("realm_id not stored: %v", m)
	}

	// Existing url_vars must be preserved when a tenant is captured post-auth.
	existingRaw := json.RawMessage(`{"url_vars":{"shop":"my-store"}}`)
	out, err = MergeMetadata(&existingRaw, map[string]interface{}{"tenant_id": "t-1"})
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(*out, &m); err != nil {
		t.Fatal(err)
	}
	if m["tenant_id"] != "t-1" {
		t.Errorf("tenant_id not stored: %v", m)
	}
	uv, ok := m["url_vars"].(map[string]interface{})
	if !ok || uv["shop"] != "my-store" {
		t.Errorf("existing url_vars not preserved: %v", m)
	}

	// A malformed existing blob starts clean rather than failing capture.
	bad := json.RawMessage(`not json`)
	out, err = MergeMetadata(&bad, map[string]interface{}{"realm_id": "9"})
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(*out, &m); err != nil {
		t.Fatal(err)
	}
	if m["realm_id"] != "9" {
		t.Errorf("realm_id not stored after malformed existing: %v", m)
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

	// An optional variable with a default (the azure-arm {tenant} shape) parses
	// its Optional/Default fields; a required one (Shopify {shop}) leaves them
	// zero-valued. The create handler uses Optional to skip the "required" check
	// and Default as the stored fallback.
	tenantRaw := json.RawMessage(`[{"key":"tenant","label":"Azure tenant (advanced)","optional":true,"default":"organizations"}]`)
	tv := CredentialProvider{URLVariablesRaw: &tenantRaw}.URLVariables()
	if len(tv) != 1 || !tv[0].Optional || tv[0].Default != "organizations" {
		t.Fatalf("optional/default not parsed: %+v", tv)
	}
	if vs[0].Optional || vs[0].Default != "" {
		t.Fatalf("required var should have zero-valued Optional/Default: %+v", vs[0])
	}
	// A concrete tenant GUID and the default keyword both pass the host-safe
	// value charset (single label of letters/digits/hyphens).
	for _, v := range []string{"organizations", "f11a332d-b270-4ce1-be01-e868c3eefd5a"} {
		if _, err := SubstituteURLVariables("https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize", map[string]string{"tenant": v}); err != nil {
			t.Errorf("SubstituteURLVariables(tenant=%q) = %v, want nil", v, err)
		}
	}
}
