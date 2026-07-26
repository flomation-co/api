package api

import (
	"encoding/json"
	"testing"
	"time"
)

// RFC 7636 Appendix B's worked example. Asserting against the published vector
// rather than against our own output is the point: a test that just re-runs the
// implementation would confirm a wrong hash just as happily as a right one.
func TestPKCEChallengeMatchesTheRFCTestVector(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	if got := PKCEChallenge(verifier); got != challenge {
		t.Errorf("S256 challenge = %q, want the RFC 7636 vector %q", got, challenge)
	}
}

func TestPKCEChallengeIsUnpaddedBase64URL(t *testing.T) {
	got := PKCEChallenge("any-verifier-at-all")
	for _, bad := range []string{"=", "+", "/"} {
		if contains(got, bad) {
			t.Errorf("challenge %q contains %q — it must be unpadded base64url, or the provider rejects it", got, bad)
		}
	}
	if len(got) != 43 {
		t.Errorf("a SHA-256 digest in unpadded base64url is 43 chars, got %d (%q)", len(got), got)
	}
}

// Salesforce's External Client Apps require PKCE and accept ONLY S256, so it must
// be in the capability table. The table exists so that adding a provider is a data
// change: the previous `if provider.Slug == "twitter"` in the URL builder is how
// Salesforce came to be documented as PKCE-enabled while sending no challenge.
func TestSalesforceRequiresPKCE(t *testing.T) {
	if !ProviderUsesPKCE("salesforce") {
		t.Error("salesforce must require PKCE — its External Client Apps default the requirement ON and reject the authorize request without a challenge")
	}
	if ProviderUsesPKCE("github") {
		t.Error("a provider not in the table must not suddenly start sending a challenge")
	}
}

// The whole point of managed auth is that a Salesforce access token dies at the
// org session timeout and something renews it. Salesforce never returns
// expires_in, a nil expiry is stored as NULL, and GetCredentialsNeedingRefresh
// filters on `token_expires_at IS NOT NULL` — so without a fallback the
// credential is never selected for refresh at all.
func TestSalesforceHasAFallbackTokenLifetime(t *testing.T) {
	ttl, ok := DefaultTokenLifetime("salesforce")
	if !ok {
		t.Fatal("salesforce must declare a fallback lifetime, or its credentials are never refreshed")
	}
	if ttl <= 0 {
		t.Errorf("fallback lifetime must be positive, got %v", ttl)
	}
	// Comfortably inside the common two-hour session timeout, so the poller
	// renews before the token actually dies rather than after.
	if ttl >= 2*time.Hour {
		t.Errorf("fallback lifetime %v is not inside the usual 2h Salesforce session timeout", ttl)
	}
}

// A provider with no declared fallback must keep storing NULL: for those, a
// missing expires_in genuinely means the token does not expire (Shopify's
// permanent tokens), and inventing an expiry would put a perfectly good
// credential into a pointless refresh loop.
func TestProvidersWithoutAFallbackKeepMeaningNeverExpires(t *testing.T) {
	for _, slug := range []string{"shopify", "github", "", "not-a-provider"} {
		if _, ok := DefaultTokenLifetime(slug); ok {
			t.Errorf("provider %q must not have a fallback lifetime", slug)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Clearing a spent secret must REMOVE the key, not blank it. A present-but-empty
// pkce_verifier reads as "this credential still has a verifier" to anyone
// auditing the record, which is the opposite of what clearing should convey.
func TestMetadataWithoutRemovesTheKeyEntirely(t *testing.T) {
	existing := json.RawMessage(`{"instance_url":"https://x.my.salesforce.com","pkce_verifier":"spent"}`)
	got, err := MetadataWithout(&existing, "pkce_verifier")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(*got, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, present := m["pkce_verifier"]; present {
		t.Errorf("the key must be absent, not blank: %s", *got)
	}
	if m["instance_url"] != "https://x.my.salesforce.com" {
		t.Errorf("removing one key must not disturb the others: %s", *got)
	}
}

func TestMetadataWithoutToleratesAbsentAndMalformedInput(t *testing.T) {
	if got, err := MetadataWithout(nil, "pkce_verifier"); err != nil || string(*got) != "{}" {
		t.Errorf("nil metadata should yield an empty object, got %v %v", got, err)
	}
	bad := json.RawMessage(`not json`)
	got, err := MetadataWithout(&bad, "pkce_verifier")
	if err != nil {
		t.Fatalf("a malformed blob must not fail the caller: %v", err)
	}
	if string(*got) != "{}" {
		t.Errorf("a malformed blob should start clean, got %s", *got)
	}
}
