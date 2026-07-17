package http

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidRoleARN(t *testing.T) {
	valid := []string{
		"arn:aws:iam::123456789012:role/FlomationAccess",
		"arn:aws:iam::123456789012:role/path/to/Role",
	}
	for _, a := range valid {
		if !validRoleARN(a) {
			t.Errorf("expected %q to be valid", a)
		}
	}
	invalid := []string{
		"",
		"not-an-arn",
		"arn:aws:s3:::my-bucket",
		"arn:aws:iam::123456789012:user/bob", // a user, not a role
		"123456789012",
	}
	for _, a := range invalid {
		if validRoleARN(a) {
			t.Errorf("expected %q to be rejected", a)
		}
	}
}

func TestGenerateExternalID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := generateExternalID()
		if !strings.HasPrefix(id, "flomation-") {
			t.Fatalf("unexpected prefix: %q", id)
		}
		if len(id) != len("flomation-")+32 { // 16 random bytes -> 32 hex chars
			t.Fatalf("unexpected length for %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate external id generated: %q", id)
		}
		seen[id] = true
	}
}

func TestBuildTrustPolicy(t *testing.T) {
	principal := "arn:aws:iam::999888777666:role/flomation-executor"
	externalID := "flomation-deadbeef"

	out := buildTrustPolicy(principal, externalID)

	// Must be valid JSON.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("trust policy is not valid JSON: %v", err)
	}

	// Must reference the principal, the external id, and the assume-role action.
	for _, want := range []string{principal, externalID, "sts:AssumeRole", "sts:ExternalId"} {
		if !strings.Contains(out, want) {
			t.Errorf("trust policy missing %q:\n%s", want, out)
		}
	}
}

func TestAWSTrustPrincipalARNFallback(t *testing.T) {
	// A Service with no AWS config returns the placeholder rather than panicking.
	s := &Service{}
	arn := s.awsTrustPrincipalARN()
	if !strings.Contains(arn, "FLOMATION_ACCOUNT_ID") {
		t.Errorf("expected placeholder ARN, got %q", arn)
	}
}
