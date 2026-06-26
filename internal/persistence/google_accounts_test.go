// Lightweight SQL-shape tests for the Google accounts persistence
// layer. CRUD behaviour requires a real Postgres connection and is
// exercised at the integration layer; here we pin the structure of
// the widened cross-channel lookup so a regression in the CTE walk
// surfaces as a string mismatch rather than a runtime "FROM user_identity
// does not exist" error in production.
package persistence

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestGetGoogleAccountsCrossChannelSQL_WalksIdentityGraph guards the
// widened-lookup CTE shape. The Andy-on-Slack-can't-see-Telegram-
// linked-calendar bug regressed because the query was a flat
// `WHERE agent_user_id = $1`. This test pins that the new query
// walks all five hops: source_handles → declared_user (via
// user_identity) → sibling_handles → sibling_users (back through
// agent_identity) → google accounts.
//
// We intentionally don't pin the exact text — only that each hop
// is named. A reader sees from the test what the contract is, and
// a refactor that renames a CTE without changing semantics will
// fail loudly so the rename is intentional.
func TestGetGoogleAccountsCrossChannelSQL_WalksIdentityGraph(t *testing.T) {
	RegisterTestingT(t)

	requiredCTEs := []string{
		"source_handles",  // hop 1: handles on the requesting agent_user
		"source_agent",    // hop 1b: the agent the requesting user belongs to
		"declared_user",   // hop 2: Flomation user(s) who claimed those handles
		"sibling_handles", // hop 3: all handles that Flomation user has claimed
		"sibling_users",   // hop 4: agent_user_ids matching those handles in the SAME agent
	}
	for _, cte := range requiredCTEs {
		Expect(strings.Contains(getGoogleAccountsCrossChannelSQL, cte)).
			To(BeTrue(), "getGoogleAccountsCrossChannelSQL missing CTE %q — identity-graph walk broken", cte)
	}
}

// TestGetGoogleAccountsCrossChannelSQL_ReferencesRequiredTables pins
// that the CTE actually joins the right tables. If a future refactor
// renames one (e.g. user_identity → declared_identity), this test
// catches the SQL drift before a deploy fails on "relation does not
// exist" against the real database.
func TestGetGoogleAccountsCrossChannelSQL_ReferencesRequiredTables(t *testing.T) {
	RegisterTestingT(t)

	requiredTables := []string{
		"agent_identity",            // per-channel agent identity
		"agent_user",                // agent-scoped person record
		"user_identity",             // user-declared channel claims
		"agent_user_google_account", // final SELECT target
	}
	for _, table := range requiredTables {
		Expect(strings.Contains(getGoogleAccountsCrossChannelSQL, table)).
			To(BeTrue(), "getGoogleAccountsCrossChannelSQL missing table %q", table)
	}
}

// TestGetGoogleAccountsCrossChannelSQL_PreservesAgentScope is the
// trickiest invariant to maintain across refactors: sibling agent_users
// must belong to the SAME agent as the requesting user. Without this,
// the lookup could return calendars linked to a DIFFERENT agent the
// same human happens to also use — that's a credentials cross-leak.
//
// The pin is the JOIN on source_agent — any rewrite that drops it
// must explicitly re-introduce the agent-id filter elsewhere or this
// test fails loud.
func TestGetGoogleAccountsCrossChannelSQL_PreservesAgentScope(t *testing.T) {
	RegisterTestingT(t)
	Expect(strings.Contains(getGoogleAccountsCrossChannelSQL, "JOIN source_agent")).
		To(BeTrue(), "sibling_users CTE must restrict to the same agent — credentials cross-agent leak otherwise")
}

// TestGetGoogleAccountsCrossChannelSQL_FallsBackToRequestingUser
// guards the backwards-compatibility invariant: a user who has NOT
// declared any user_identity rows must still get exactly the legacy
// behaviour (their own agent_user's accounts). The UNION on $1 in
// sibling_users is what gives us that. Without it, declared-user
// chain failure would silently hide a user's own linked calendar.
func TestGetGoogleAccountsCrossChannelSQL_FallsBackToRequestingUser(t *testing.T) {
	RegisterTestingT(t)
	// The fallback is the second arm of the UNION inside sibling_users.
	// Pinned via the literal "UNION" + "$1::UUID" pair so a refactor
	// that drops the fallback fails this test.
	Expect(strings.Contains(getGoogleAccountsCrossChannelSQL, "UNION")).To(BeTrue())
	Expect(strings.Contains(getGoogleAccountsCrossChannelSQL, "$1::UUID")).
		To(BeTrue(), "sibling_users must always include the requesting agent_user_id as a fallback")
}
