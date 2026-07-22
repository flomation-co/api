package http

import (
	"testing"

	. "github.com/onsi/gomega"
)

// managedCredentialName extracts the credential name from a ${credentials.X}
// reference used by the resource-autocomplete proxy for managed AWS Role creds.
func TestManagedCredentialName(t *testing.T) {
	RegisterTestingT(t)

	Expect(managedCredentialName("${credentials.FLOMATION_AWS_ADMIN}")).To(Equal("FLOMATION_AWS_ADMIN"))
	Expect(managedCredentialName("${credential.my-role}")).To(Equal("my-role"))
	Expect(managedCredentialName("  ${credentials.abc}  ")).To(Equal("abc"))

	// Not a managed-credential ref → empty (falls back to the "type it in" message).
	Expect(managedCredentialName("${secrets.AWS_KEY}")).To(Equal(""))
	Expect(managedCredentialName("AKIAEXAMPLE")).To(Equal(""))
	Expect(managedCredentialName("")).To(Equal(""))
	Expect(managedCredentialName("${credentials.unterminated")).To(Equal(""))
}
