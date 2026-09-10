package http

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// sanitisePermissionLevels defensively bounds the aws_role permission-level map
// before it lands in the credential's metadata JSONB. These pin the guards.
func TestSanitisePermissionLevels(t *testing.T) {
	RegisterTestingT(t)

	// Trims whitespace and keeps valid entries.
	got := sanitisePermissionLevels(map[string]string{"ec2": " manage ", " s3 ": "read"})
	Expect(got).To(Equal(map[string]string{"ec2": "manage", "s3": "read"}))

	// Drops empty keys/values.
	got = sanitisePermissionLevels(map[string]string{"ec2": "", "": "read", "iam": "  "})
	Expect(got).To(BeEmpty())

	// Never returns nil (so metadata stores {} not JSON null).
	got = sanitisePermissionLevels(nil)
	Expect(got).ToNot(BeNil())
	Expect(got).To(BeEmpty())

	// Over-length keys/values are dropped.
	got = sanitisePermissionLevels(map[string]string{
		strings.Repeat("x", 100): "manage",
		"kms":                    strings.Repeat("y", 100),
		"rds":                    "full",
	})
	Expect(got).To(Equal(map[string]string{"rds": "full"}))

	// Entry count is capped.
	big := map[string]string{}
	for i := 0; i < 200; i++ {
		big[strings.Repeat("a", 1)+string(rune('A'+i%26))+string(rune('0'+i/26))] = "read"
	}
	got = sanitisePermissionLevels(big)
	Expect(len(got)).To(BeNumerically("<=", 64))
}
