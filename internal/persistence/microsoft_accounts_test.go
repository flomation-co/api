// Lightweight SQL-shape tests for the Microsoft accounts persistence
// layer. Mirror of google_accounts_test.go — pins the same identity-
// graph walk + agent-scope invariants on the parallel Microsoft
// implementation so when Outlook/Teams agent actions are wired up,
// the bug we fixed for Google can't silently reappear.
package persistence

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestGetMicrosoftAccountsCrossChannelSQL_WalksIdentityGraph pins
// every named CTE in the chain. Same contract as the Google version
// — see google_accounts_test.go for the rationale.
func TestGetMicrosoftAccountsCrossChannelSQL_WalksIdentityGraph(t *testing.T) {
	RegisterTestingT(t)

	requiredCTEs := []string{
		"source_handles",
		"source_agent",
		"declared_user",
		"sibling_handles",
		"sibling_users",
	}
	for _, cte := range requiredCTEs {
		Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, cte)).
			To(BeTrue(), "getMicrosoftAccountsCrossChannelSQL missing CTE %q", cte)
	}
}

// TestGetMicrosoftAccountsCrossChannelSQL_ReferencesRequiredTables
// pins the joins. The final SELECT targets microsoft_account (not
// agent_user_google_account) — that's the one column-name difference
// from the Google version, and the test catches accidental
// copy-paste of the Google SELECT.
func TestGetMicrosoftAccountsCrossChannelSQL_ReferencesRequiredTables(t *testing.T) {
	RegisterTestingT(t)

	requiredTables := []string{
		"agent_identity",
		"agent_user",
		"user_identity",
		"microsoft_account",
	}
	for _, table := range requiredTables {
		Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, table)).
			To(BeTrue(), "getMicrosoftAccountsCrossChannelSQL missing table %q", table)
	}

	// Defensive: must NOT reference the Google table — that would
	// indicate a botched copy/paste returning the wrong rows.
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "agent_user_google_account")).
		To(BeFalse(), "Microsoft query must NOT reference the Google table")
}

// TestGetMicrosoftAccountsCrossChannelSQL_PreservesAgentScope —
// same critical invariant as the Google version. Without source_agent
// the sibling_users CTE would return agent_users across multiple
// agents, which is a credentials cross-leak.
func TestGetMicrosoftAccountsCrossChannelSQL_PreservesAgentScope(t *testing.T) {
	RegisterTestingT(t)
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "JOIN source_agent")).
		To(BeTrue(), "sibling_users CTE must restrict to the same agent — credentials cross-agent leak otherwise")
}

// TestGetMicrosoftAccountsCrossChannelSQL_FallsBackToRequestingUser
// guards backwards compatibility for users with no user_identity
// declarations.
func TestGetMicrosoftAccountsCrossChannelSQL_FallsBackToRequestingUser(t *testing.T) {
	RegisterTestingT(t)
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "UNION")).To(BeTrue())
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "$1::UUID")).
		To(BeTrue(), "sibling_users must always include the requesting agent_user_id as a fallback")
}

// TestGetMicrosoftAccountsCrossChannelSQL_DecryptsTokens pins the
// encryption-at-rest contract introduced by migration 102. Both
// access_token AND refresh_token must be wrapped in PGP_SYM_DECRYPT
// — if either is missing, callers would either crash on a BYTEA
// value where they expect plaintext, or (worse) silently return
// encrypted bytes that the OAuth client would reject without a
// useful error.
func TestGetMicrosoftAccountsCrossChannelSQL_DecryptsTokens(t *testing.T) {
	RegisterTestingT(t)
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "PGP_SYM_DECRYPT(access_token")).
		To(BeTrue(), "access_token must be decrypted at SELECT time (migration 102 stores it as BYTEA)")
	Expect(strings.Contains(getMicrosoftAccountsCrossChannelSQL, "PGP_SYM_DECRYPT(refresh_token")).
		To(BeTrue(), "refresh_token must be decrypted at SELECT time (migration 102 stores it as BYTEA)")
}
