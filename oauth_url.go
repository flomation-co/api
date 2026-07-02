package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

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
