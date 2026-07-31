package persistence

import (
	"sort"
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func mapping(idp, team string) *api.SSOGroupMapping {
	return &api.SSOGroupMapping{IDPGroup: idp, OrganisationGroupID: team}
}

func TestReconcileDecision_AddsMappedRemovesUnmapped(t *testing.T) {
	RegisterTestingT(t)
	mappings := []*api.SSOGroupMapping{
		mapping("aad-eng", "team-eng"),   // engineering
		mapping("aad-fin", "team-fin"),   // finance
	}
	// User is in the eng IdP group; currently a member of finance (SSO-managed)
	// but no longer qualifies, and of a manually-managed team.
	add, remove := ssoGroupReconcileDecision(mappings, []string{"aad-eng"}, []string{"team-fin", "team-manual"})

	Expect(add).To(Equal([]string{"team-eng"}))       // qualifies now, not a member → add
	Expect(remove).To(Equal([]string{"team-fin"}))    // no longer qualifies → remove
	// team-manual is NOT a mapping target → untouched (not in add or remove).
	Expect(add).ToNot(ContainElement("team-manual"))
	Expect(remove).ToNot(ContainElement("team-manual"))
}

func TestReconcileDecision_NoChangeWhenAlreadyCorrect(t *testing.T) {
	RegisterTestingT(t)
	mappings := []*api.SSOGroupMapping{mapping("g", "t")}
	add, remove := ssoGroupReconcileDecision(mappings, []string{"g"}, []string{"t"})
	Expect(add).To(BeEmpty())
	Expect(remove).To(BeEmpty())
}

func TestReconcileDecision_ManyToMany(t *testing.T) {
	RegisterTestingT(t)
	// One IdP group maps to two Teams; a second Team is mapped from a group the
	// user isn't in and they're currently a member → removed.
	mappings := []*api.SSOGroupMapping{
		mapping("admins", "team-a"),
		mapping("admins", "team-b"),
		mapping("other", "team-c"),
	}
	add, remove := ssoGroupReconcileDecision(mappings, []string{"admins"}, []string{"team-c"})
	sort.Strings(add)
	Expect(add).To(Equal([]string{"team-a", "team-b"}))
	Expect(remove).To(Equal([]string{"team-c"}))
}

func TestReconcileDecision_NoMappingsNoOp(t *testing.T) {
	RegisterTestingT(t)
	add, remove := ssoGroupReconcileDecision(nil, []string{"g"}, []string{"t"})
	Expect(add).To(BeEmpty())
	Expect(remove).To(BeEmpty())
}
