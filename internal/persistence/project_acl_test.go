package persistence

import (
	"testing"

	. "github.com/onsi/gomega"
)

func sp(s string) *string { return &s }

// Two projects: parent → child, plus a standalone. Reused across cases.
func baseRows() []projectRow {
	return []projectRow{
		{ID: "parent", ParentID: nil, OwnerID: sp("owner")},
		{ID: "child", ParentID: sp("parent"), OwnerID: sp("owner")},
		{ID: "solo", ParentID: nil, OwnerID: sp("owner")},
	}
}

func TestComputeAccess_OpenProjectVisibleToEveryone(t *testing.T) {
	RegisterTestingT(t)
	acc := computeProjectAccess(baseRows(), nil, map[string]bool{}, "stranger", false)

	for _, id := range []string{"parent", "child", "solo"} {
		Expect(acc[id].Accessible).To(BeTrue(), id)
		Expect(acc[id].Restricted).To(BeFalse(), id)
		Expect(acc[id].Role).To(Equal("view"), id)
	}
}

func TestComputeAccess_RestrictedHidesNonMembers(t *testing.T) {
	RegisterTestingT(t)
	grants := []projectGrant{{ProjectID: "solo", GroupID: "team-a", Role: "edit"}}

	// A user in no team cannot see the restricted project.
	stranger := computeProjectAccess(baseRows(), grants, map[string]bool{}, "stranger", false)
	Expect(stranger["solo"].Restricted).To(BeTrue())
	Expect(stranger["solo"].Accessible).To(BeFalse())
	Expect(stranger["solo"].Role).To(Equal(""))
	// The other projects stay open.
	Expect(stranger["parent"].Accessible).To(BeTrue())

	// A member of the granted team gets in, at the granted role.
	member := computeProjectAccess(baseRows(), grants, map[string]bool{"team-a": true}, "stranger", false)
	Expect(member["solo"].Accessible).To(BeTrue())
	Expect(member["solo"].Role).To(Equal("edit"))
}

func TestComputeAccess_RestrictionInheritsToDescendants(t *testing.T) {
	RegisterTestingT(t)
	// Grant lives on the PARENT only; the child must inherit the restriction.
	grants := []projectGrant{{ProjectID: "parent", GroupID: "team-a", Role: "view"}}

	stranger := computeProjectAccess(baseRows(), grants, map[string]bool{}, "stranger", false)
	Expect(stranger["parent"].Accessible).To(BeFalse())
	Expect(stranger["child"].Restricted).To(BeTrue())
	Expect(stranger["child"].Accessible).To(BeFalse())

	member := computeProjectAccess(baseRows(), grants, map[string]bool{"team-a": true}, "stranger", false)
	Expect(member["parent"].Accessible).To(BeTrue())
	Expect(member["child"].Accessible).To(BeTrue())
	Expect(member["child"].Role).To(Equal("view"))
}

func TestComputeAccess_RoleIsMaxAcrossChain(t *testing.T) {
	RegisterTestingT(t)
	// Parent grants view, child grants manage (same team). The child's effective
	// role is the strongest across the chain.
	grants := []projectGrant{
		{ProjectID: "parent", GroupID: "team-a", Role: "view"},
		{ProjectID: "child", GroupID: "team-a", Role: "manage"},
	}
	acc := computeProjectAccess(baseRows(), grants, map[string]bool{"team-a": true}, "stranger", false)
	Expect(acc["child"].Role).To(Equal("manage"))
	Expect(acc["parent"].Role).To(Equal("view"))
}

func TestComputeAccess_AdminAndOwnerAlwaysManage(t *testing.T) {
	RegisterTestingT(t)
	grants := []projectGrant{{ProjectID: "solo", GroupID: "team-a", Role: "view"}}

	// Admin sees everything at manage, regardless of grants/teams.
	admin := computeProjectAccess(baseRows(), grants, map[string]bool{}, "stranger", true)
	Expect(admin["solo"].Accessible).To(BeTrue())
	Expect(admin["solo"].Role).To(Equal("manage"))
	Expect(admin["solo"].Restricted).To(BeTrue()) // still reported as restricted

	// The owner reaches their own restricted project without a team grant.
	owner := computeProjectAccess(baseRows(), grants, map[string]bool{}, "owner", false)
	Expect(owner["solo"].Accessible).To(BeTrue())
	Expect(owner["solo"].Role).To(Equal("manage"))
}
