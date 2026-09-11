package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// basicAuthTokenProviders lists providers whose OAuth token endpoint expects
// the client credentials via HTTP Basic auth (Authorization: Basic
// base64(client_id:client_secret)) rather than in the form body. Intuit
// (QuickBooks) REQUIRES this and rejects body credentials; Xero accepts it and
// documents it as the preferred method. Both the authorization-code exchange
// and the refresh-token grant must use the same auth style, so this lives in
// the shared api package for the http handler and the refresh poller to share.
var basicAuthTokenProviders = map[string]bool{
	"quickbooks": true,
	"xero":       true,
}

// ProviderUsesBasicAuth reports whether the provider's token endpoint expects
// client credentials via HTTP Basic auth instead of in the request body.
func ProviderUsesBasicAuth(slug string) bool {
	return basicAuthTokenProviders[slug]
}

// pkceS256Providers lists providers whose authorization-code flow must carry a
// PKCE challenge using the S256 method, with the matching code_verifier on the
// exchange.
//
// Salesforce is here because its External Client Apps ship with "Require Proof
// Key for Code Exchange (PKCE) Extension for Supported Authorization Flows"
// switched ON by default, and only S256 is accepted — the "plain" method used by
// the older Twitter path is rejected. Without a challenge the authorization
// request is refused outright, so every managed connect would die on the first
// click.
//
// Deliberately a capability table rather than a slug equality check in the URL
// builder: the previous `if provider.Slug == "twitter"` meant adding a provider
// required editing control flow, which is exactly how Salesforce ended up
// documented as PKCE-enabled while sending no challenge at all.
var pkceS256Providers = map[string]bool{
	"salesforce": true,
}

// ProviderUsesPKCE reports whether the provider requires an S256 PKCE challenge
// on authorize and the matching verifier on the token exchange.
func ProviderUsesPKCE(slug string) bool {
	return pkceS256Providers[slug]
}

// defaultTokenLifetimes gives a fallback access-token lifetime for providers
// whose token response omits expires_in.
//
// Salesforce never returns expires_in — it returns issued_at instead — so
// token_expires_at was written as NULL, and GetCredentialsNeedingRefresh filters
// on `token_expires_at IS NOT NULL`. The credential was therefore NEVER selected
// for refresh: it connected cleanly, reported "active", and then every call
// started failing with INVALID_SESSION_ID at the org's session timeout while the
// credential still displayed as healthy with no last_error. That is precisely the
// failure managed auth exists to prevent.
//
// One hour is deliberately conservative: the common Salesforce session timeout is
// two hours, and the refresh poller's lookahead means a credential is renewed
// well before the stamp. Refreshing earlier than strictly necessary costs one
// call; refreshing too late costs the customer a dead flow.
// A real limit worth stating rather than leaving implicit: an org that has
// SHORTENED its session timeout below this can still expire before the poller
// renews. One hour suits the default two-hour timeout; an org set to 30 minutes
// would see failures between the real expiry and our assumed one. The proper fix
// is to read issued_at and the org's own session policy, which Salesforce does
// not expose on the token response — so this is a floor, not a guarantee, and
// the symptom would be intermittent INVALID_SESSION_ID on a credential that
// still reads "active".
var defaultTokenLifetimes = map[string]time.Duration{
	"salesforce": time.Hour,
}

// DefaultTokenLifetime returns the fallback lifetime for a provider that omits
// expires_in, and false when the provider has none — in which case a missing
// expires_in genuinely means "does not expire" (Shopify's permanent tokens) and
// must keep storing NULL.
func DefaultTokenLifetime(slug string) (time.Duration, bool) {
	d, ok := defaultTokenLifetimes[slug]
	return d, ok
}

// URLVariable describes a per-credential value substituted into a provider's
// OAuth URLs. Providers whose OAuth endpoints are per-tenant declare these —
// e.g. Shopify's shop subdomain in https://{shop}.myshopify.com/admin/oauth/
// authorize. The editor prompts for each value when a credential is created;
// the value is stored on the credential and substituted at authorize,
// token-exchange, and refresh time. Reusable for any per-tenant SaaS
// (Shopify, Zendesk's {subdomain}.zendesk.com, self-hosted instances, ...).
type URLVariable struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	// Optional variables may be left blank at connect time; when blank, Default
	// is substituted. Used for values that have a sensible fallback the operator
	// only overrides in special cases — e.g. Azure's {tenant}, which defaults to
	// "organizations" (any work/school tenant) but can be pinned to a specific
	// tenant ID for guest/cross-tenant sign-in. A required variable (the default)
	// has no fallback and must be supplied (e.g. Shopify's {shop}).
	Optional bool   `json:"optional,omitempty"`
	Default  string `json:"default,omitempty"`
}

// urlVarValuePattern restricts a variable value to a single DNS label
// (letters, numbers, hyphens) so a crafted value can never inject a different
// host into an OAuth URL — the value only ever fills a subdomain slot before a
// fixed provider domain.
var urlVarValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// SubstituteURLVariables replaces {key} placeholders in a provider OAuth URL
// with per-credential values, validating each value against the host-safe
// charset. A URL with no placeholders is returned unchanged (the common case
// for the fixed-URL providers). Returns an error if any placeholder is left
// unfilled or a value is unsafe.
func SubstituteURLVariables(rawURL string, vars map[string]string) (string, error) {
	out := rawURL
	for k, v := range vars {
		if !urlVarValuePattern.MatchString(v) {
			return "", fmt.Errorf("invalid value for %q: only letters, numbers and hyphens are allowed", k)
		}
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.Index(out[i:], "}"); j >= 0 {
			return "", fmt.Errorf("missing value for URL variable %s", out[i:i+j+1])
		}
		return "", fmt.Errorf("malformed provider URL: %s", out)
	}
	return out, nil
}

// ValidateURLVars checks that a credential's stored URL variable values still
// satisfy the provider's current url_variables declaration. It exists for the
// re-authorise path: a credential created before a provider added a required
// variable would otherwise only fail deep inside SubstituteURLVariables with a
// generic "missing value for URL variable {x}" message. This surfaces the same
// case up front, naming the variable and its label so the user knows exactly
// what to re-enter. Extra stored keys (a provider that dropped a variable) are
// ignored — they simply no-op during substitution.
func (p CredentialProvider) ValidateURLVars(vars map[string]string) error {
	for _, v := range p.URLVariables() {
		if strings.TrimSpace(vars[v.Key]) == "" {
			return fmt.Errorf("this credential predates a change to %s and is missing the %q value (%s) — recreate the credential to set it", p.Name, v.Key, v.Label)
		}
	}
	return nil
}

// URLVariables parses the provider's declared url_variables JSON column.
func (p CredentialProvider) URLVariables() []URLVariable {
	if p.URLVariablesRaw == nil {
		return nil
	}
	var vs []URLVariable
	if err := json.Unmarshal(*p.URLVariablesRaw, &vs); err != nil {
		return nil
	}
	return vs
}

// URLVarsFromMetadata reads the stored per-credential URL variable values from
// a credential's metadata JSON ({"url_vars": {"shop": "my-store"}}).
func URLVarsFromMetadata(metadata *json.RawMessage) map[string]string {
	if metadata == nil {
		return nil
	}
	var m struct {
		URLVars map[string]string `json:"url_vars"`
	}
	if err := json.Unmarshal(*metadata, &m); err != nil {
		return nil
	}
	return m.URLVars
}

// MergeMetadata merges the given key/values into a credential's existing
// metadata JSON, preserving any keys already present (e.g. url_vars). Returns a
// fresh json.RawMessage. Used by the OAuth callback to record the per-account
// identifier discovered after authorisation (realm_id / tenant_id / …) without
// clobbering pre-auth values.
func MergeMetadata(existing *json.RawMessage, kv map[string]interface{}) (*json.RawMessage, error) {
	merged := map[string]interface{}{}
	if existing != nil && len(*existing) > 0 {
		if err := json.Unmarshal(*existing, &merged); err != nil {
			// A malformed/unknown-shape metadata blob shouldn't block capture;
			// start clean rather than fail the whole authorisation.
			merged = map[string]interface{}{}
		}
	}
	for k, v := range kv {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(b)
	return &raw, nil
}

// MetadataWithURLVars builds the metadata JSON that stores URL variable values,
// or nil when there are none.
func MetadataWithURLVars(vars map[string]string) (*json.RawMessage, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(map[string]interface{}{"url_vars": vars})
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(b)
	return &raw, nil
}

// PKCEVerifierFromMetadata reads the stored code_verifier back off a credential
// at exchange time. It lives in metadata alongside url_vars rather than in a
// separate table because the authorize leg and the callback share nothing else —
// the callback's only handle on the flow is the credential id carried in `state`.
func PKCEVerifierFromMetadata(metadata *json.RawMessage) string {
	if metadata == nil {
		return ""
	}
	var m struct {
		PKCEVerifier string `json:"pkce_verifier"`
	}
	if err := json.Unmarshal(*metadata, &m); err != nil {
		return ""
	}
	return m.PKCEVerifier
}

// PKCEChallenge derives the S256 challenge from a verifier:
// BASE64URL(SHA256(verifier)), unpadded, per RFC 7636. Salesforce accepts only
// this method — sending the verifier itself as the challenge ("plain") is
// rejected.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// MetadataWithout returns a credential's metadata with the named keys REMOVED,
// rather than blanked.
//
// MergeMetadata can only add or overwrite, so clearing a key by setting it to ""
// leaves it present and empty. For a spent secret that is a poor result: the key
// lingers in the record, and anyone auditing "does this credential still hold a
// verifier" gets a misleading yes. Removing it is what "cleared" should mean.
func MetadataWithout(existing *json.RawMessage, keys ...string) (*json.RawMessage, error) {
	merged := map[string]interface{}{}
	if existing != nil && len(*existing) > 0 {
		if err := json.Unmarshal(*existing, &merged); err != nil {
			// Matching MergeMetadata: a malformed blob starts clean rather than
			// failing the caller.
			merged = map[string]interface{}{}
		}
	}
	for _, k := range keys {
		delete(merged, k)
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(b)
	return &raw, nil
}
