package http

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

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

// managedCredFromQuery detects a managed AWS Role credential from the request —
// from the `credential` input or the auto-filled `aws_secret_key` accessor, but
// NOT from `aws_access_key` (which carries the .base_access_key_id sub-accessor).
func TestManagedCredFromQuery(t *testing.T) {
	RegisterTestingT(t)

	ctx := func(qs string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/?"+qs, nil)
		return c
	}

	// From the credential input.
	Expect(managedCredFromQuery(ctx("credential=" + urlq("${credentials.MY_ROLE}")))).To(Equal("MY_ROLE"))
	// From the auto-filled secret-key accessor when credential is absent.
	Expect(managedCredFromQuery(ctx("aws_secret_key=" + urlq("${credentials.MY_ROLE}")))).To(Equal("MY_ROLE"))
	// aws_access_key alone (with the .base_access_key_id accessor) does NOT count.
	Expect(managedCredFromQuery(ctx("aws_access_key=" + urlq("${credentials.MY_ROLE.base_access_key_id}")))).To(Equal(""))
	// Plain pasted keys / env secrets are not managed.
	Expect(managedCredFromQuery(ctx("aws_secret_key=" + urlq("${secrets.AWS}")))).To(Equal(""))
	Expect(managedCredFromQuery(ctx(""))).To(Equal(""))
}

func urlq(s string) string { return url.QueryEscape(s) }
