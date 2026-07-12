package persistence

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func TestGenerateEmbedPublishableKey(t *testing.T) {
	RegisterTestingT(t)

	k := GenerateEmbedPublishableKey()
	Expect(strings.HasPrefix(k, "pk_")).To(BeTrue())
	Expect(len(k)).To(Equal(len("pk_") + embedPublishableKeyBytes))
	// Two keys must differ (unguessable random suffix).
	Expect(GenerateEmbedPublishableKey()).ToNot(Equal(k))
}

func TestDedupeEmbedOrigins(t *testing.T) {
	RegisterTestingT(t)

	// Blanks dropped, duplicates collapsed, first-seen order preserved.
	got := dedupeEmbedOrigins([]string{"https://a.com", "", "https://b.com", "https://a.com", ""})
	Expect(got).To(Equal([]string{"https://a.com", "https://b.com"}))
	Expect(dedupeEmbedOrigins(nil)).To(BeEmpty())
}

func TestEmbedScope(t *testing.T) {
	RegisterTestingT(t)

	// Personal scope: owner + NULL org, bound at the given index.
	pred, args := embedScope("owner-1", nil, 1)
	Expect(pred).To(Equal("owner_id = $1 AND organisation_id IS NULL"))
	Expect(args).To(Equal([]interface{}{"owner-1"}))

	// Org scope: organisation_id at the given index.
	org := "org-9"
	pred, args = embedScope("owner-1", &org, 2)
	Expect(pred).To(Equal("organisation_id = $2"))
	Expect(args).To(Equal([]interface{}{"org-9"}))
}
