package persistence

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// The root-only executions list (used by the hierarchical executions UI
// via root_only=true) builds its SQL dynamically. This suite pins the
// organisation scoping so a regression can't silently leak org-owned
// executions into a user's personal view.
//
// Bug fixed: the personal branch filtered only e.owner_id, missing
// AND e.organisation_id IS NULL — so an execution of an org-owned flow
// the user authored (owner_id = user, organisation_id = org) appeared in
// their personal context.

func TestBuildRootExecutionsQueries_PersonalScopesToOrgLess(t *testing.T) {
	RegisterTestingT(t)

	selectSQL, countSQL, selectArgs, countArgs := buildRootExecutionsQueries(0, 10, "", "user-123", nil, false)

	// Both the select and the count MUST restrict to the user's own
	// organisation-less executions.
	Expect(selectSQL).To(ContainSubstring("e.owner_id = $1"))
	Expect(selectSQL).To(ContainSubstring("e.organisation_id IS NULL"),
		"personal select must exclude org-owned executions")
	Expect(countSQL).To(ContainSubstring("e.owner_id = $1"))
	Expect(countSQL).To(ContainSubstring("e.organisation_id IS NULL"),
		"personal count must exclude org-owned executions")

	// The personal branch must NOT filter by a specific organisation id.
	Expect(selectSQL).ToNot(ContainSubstring("e.organisation_id = $"))
	Expect(countSQL).ToNot(ContainSubstring("e.organisation_id = $"))

	// Only the owner id is bound as a filter arg (offset/limit follow on
	// the select query).
	Expect(countArgs).To(Equal([]interface{}{"user-123"}))
	Expect(selectArgs).To(Equal([]interface{}{"user-123", int64(0), int64(10)}))
}

func TestBuildRootExecutionsQueries_OrgScopesToOrganisation(t *testing.T) {
	RegisterTestingT(t)

	orgID := "org-abc"
	selectSQL, countSQL, selectArgs, countArgs := buildRootExecutionsQueries(5, 20, "", "user-123", &orgID, true)

	// Organisation context filters by organisation_id, never by owner and
	// never with the org-less guard.
	Expect(selectSQL).To(ContainSubstring("e.organisation_id = $1"))
	Expect(countSQL).To(ContainSubstring("e.organisation_id = $1"))
	Expect(selectSQL).ToNot(ContainSubstring("e.organisation_id IS NULL"))
	// owner_id appears in the SELECT column list; assert it is not used
	// as a WHERE filter (the "= $" bound form).
	Expect(selectSQL).ToNot(ContainSubstring("e.owner_id = $"))
	Expect(countSQL).ToNot(ContainSubstring("e.owner_id = $"))

	Expect(countArgs).To(Equal([]interface{}{orgID}))
	Expect(selectArgs).To(Equal([]interface{}{orgID, int64(5), int64(20)}))
}

func TestBuildRootExecutionsQueries_SearchKeepsOrgLessGuard(t *testing.T) {
	RegisterTestingT(t)

	// With a search term the personal branch must STILL carry the
	// org-less guard — the search predicate is additive, not a
	// replacement for the scoping.
	selectSQL, countSQL, selectArgs, countArgs := buildRootExecutionsQueries(0, 10, "invoice", "user-123", nil, false)

	Expect(selectSQL).To(ContainSubstring("e.organisation_id IS NULL"))
	Expect(countSQL).To(ContainSubstring("e.organisation_id IS NULL"))
	Expect(selectSQL).To(ContainSubstring("LIKE LOWER($2)"))

	// owner id, then the wildcarded search term; select adds offset/limit.
	Expect(countArgs).To(Equal([]interface{}{"user-123", "%invoice%"}))
	Expect(selectArgs).To(Equal([]interface{}{"user-123", "%invoice%", int64(0), int64(10)}))

	// The count query must not accidentally inherit OFFSET/LIMIT.
	Expect(strings.Contains(countSQL, "LIMIT")).To(BeFalse())
}
