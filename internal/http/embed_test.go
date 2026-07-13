package http

import (
	"testing"

	. "github.com/onsi/gomega"

	"flomation.app/automate/api"
)

func TestEmbedResourceTypeValid(t *testing.T) {
	RegisterTestingT(t)

	Expect(embedResourceTypeValid(api.EmbedResourceForm)).To(BeTrue())
	Expect(embedResourceTypeValid(api.EmbedResourceFlow)).To(BeTrue())
	Expect(embedResourceTypeValid(api.EmbedResourceAgent)).To(BeTrue())
	// Anything else is rejected so a crafted resource_type can't reach the DB.
	Expect(embedResourceTypeValid("secret")).To(BeFalse())
	Expect(embedResourceTypeValid("")).To(BeFalse())
	Expect(embedResourceTypeValid("Form")).To(BeFalse())
}

func TestOrgForUser(t *testing.T) {
	RegisterTestingT(t)

	Expect(orgForUser(nil)).To(BeNil())
	Expect(orgForUser(&api.User{ID: "u1"})).To(BeNil())

	u := &api.User{ID: "u1", Organisations: []api.Organisation{{ID: "org-1"}}}
	got := orgForUser(u)
	Expect(got).ToNot(BeNil())
	Expect(*got).To(Equal("org-1"))
}
