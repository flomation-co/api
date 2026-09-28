package config

import "testing"

// The default app URL is what every link in an onboarding email is built
// from. It shipped as www.flomation.app, which does not resolve — DNS has no
// record for it at all — so every button in those emails would have gone
// nowhere. flomation.app exists but only as a redirect to editor.flomation.app.
//
// This pins the value rather than trusting a comment, because the failure is
// invisible from inside the code: the emails render, send and look right, and
// only the recipient finds out.
func TestDefaultAppURLIsTheHostThatActuallyResolves(t *testing.T) {
	if defaultAppURL != "https://editor.flomation.app" {
		t.Fatalf("default app URL is %q; onboarding email links are built from this, "+
			"and www.flomation.app in particular has no DNS record", defaultAppURL)
	}
}

func TestAppURLPrefersConfiguration(t *testing.T) {
	c := &Config{App: &AppConfig{URL: "https://editor.staging.example.com"}}
	if got := c.AppURL(); got != "https://editor.staging.example.com" {
		t.Fatalf("configured app URL was ignored: got %q", got)
	}

	// An absent or blank block falls back rather than producing links with an
	// empty host.
	if got := (&Config{}).AppURL(); got != defaultAppURL {
		t.Fatalf("missing app block: got %q, want the default", got)
	}
	if got := (&Config{App: &AppConfig{}}).AppURL(); got != defaultAppURL {
		t.Fatalf("blank app URL: got %q, want the default", got)
	}
}
