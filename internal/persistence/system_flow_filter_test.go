package persistence

// Phase 1.6 of the Agent Memory feature — regression guardrail for the
// default system_flow filter on flow-list and execution-list queries.
//
// The persistence package has no DB-backed tests (every higher-level
// test mocks this layer), so instead of asserting on query *results* we
// assert on the query *text*: every SQL clause that references the
// `flo` table via `f.archived_at IS NULL` must also carry the
// `f.system_flow = FALSE` filter so that system flows (and their
// executions) are invisible to standard list endpoints by default.
//
// If someone later adds a new list query that forgets the filter, this
// test fails immediately with a pointer to the offending clause.

import (
	"os"
	"strings"
	"testing"
)

// TestSystemFlowFilterAppliedToAllListQueries walks every line of
// service.go that mentions `f.archived_at IS NULL` and asserts the
// system_flow filter is present on the same line. The archived-at
// clause is a reliable marker because it appears exactly once per
// list/join query and nowhere else — the two filters travel together
// as a canonical pair.
func TestSystemFlowFilterAppliedToAllListQueries(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("failed to read service.go: %v", err)
	}
	src := string(b)

	const archivedMarker = "f.archived_at IS NULL"
	const systemFlowMarker = "f.system_flow = FALSE"

	// An explicit opt-out marker for queries that legitimately need
	// system flows included alongside non-archived user flows. The
	// canonical example is the trigger fan-out query
	// (stmtGetFlosForTrigger) — every trigger fire reads it to decide
	// which executions to create, and the Agent Memory Extraction
	// system flow's manual trigger MUST fire that path. The marker
	// must appear on the same line as the archived clause so it's
	// visually impossible to miss when reading the query.
	//
	// The marker MUST NOT contain a ':' character: sqlx's named-
	// statement preparer regex-scans the SQL text (including inside
	// SQL comments) for `:identifier` patterns and would treat
	// `:system` as a required bound parameter, breaking every Exec
	// on the prepared statement at runtime.
	const allowSystemFlowsMarker = "-- dispatch-path-system-flows-allowed"

	lines := strings.Split(src, "\n")
	var missing []string
	found := 0
	for i, line := range lines {
		if !strings.Contains(line, archivedMarker) {
			continue
		}
		found++
		if strings.Contains(line, allowSystemFlowsMarker) {
			continue
		}
		if !strings.Contains(line, systemFlowMarker) {
			missing = append(missing, lineRef(i+1, line))
		}
	}

	if found == 0 {
		t.Fatal("expected at least one f.archived_at IS NULL clause in service.go — marker may have moved; update the test")
	}
	if len(missing) > 0 {
		t.Fatalf("these %d list/join clauses are missing the system_flow filter:\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestNoColonIdentifiersInSQLComments guards against an entire class
// of sqlx-PrepareNamed regression. The named-statement preparer
// regex-scans the SQL TEXT for `:identifier` patterns and binds each
// match to a struct field at Exec time — without respecting SQL
// comment scopes. So a stray `-- explainer:topic` line silently turns
// into a required `:explainer` parameter and every dispatch returns
// 500 with "could not find name explainer in ...".
//
// Triggered by the dispatch-path-system-flows-allowed marker bug from
// commit bbadfd4 — the original marker contained a colon, sqlx parsed
// `:system` as a parameter, and every Telegram/Slack/Teams dispatch
// against an agent's orchestrator started failing in production.
//
// The check is narrow on purpose: it only flags `--` line comments
// inside raw SQL strings that contain `:<letter>` patterns. The
// false-positive surface is essentially zero in practice — nobody
// writes `time-of-day:14:00` in a SQL comment — and the cost of
// missing one is a production outage on every dispatch.
func TestNoColonIdentifiersInSQLComments(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("failed to read service.go: %v", err)
	}
	src := string(b)

	lines := strings.Split(src, "\n")
	var offences []string
	for i, line := range lines {
		// Only look at lines that look like they're inside a SQL
		// backtick block (heuristic: contain a `--` SQL line comment
		// AND start with whitespace, ruling out Go `//` style code
		// comments and string literals on assignment lines).
		commentIdx := strings.Index(line, "--")
		if commentIdx < 0 {
			continue
		}
		// Skip lines that are pure Go-style comments (start of trimmed
		// line is `//` or `/*`). Those are never inside SQL strings.
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		comment := line[commentIdx+2:]
		// Look for :letter — sqlx terminates parameter names at the
		// first non-alphanumeric/underscore character, so the colon
		// followed by a letter is the dangerous shape. Allow `::` as
		// it's the Postgres cast operator (e.g. `::uuid`).
		for j := 0; j < len(comment)-1; j++ {
			if comment[j] != ':' {
				continue
			}
			if j+1 < len(comment) && comment[j+1] == ':' {
				// `::` cast, not a binding.
				continue
			}
			next := comment[j+1]
			if (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || next == '_' {
				offences = append(offences, lineRef(i+1, line))
				break
			}
		}
	}

	if len(offences) > 0 {
		t.Fatalf("these %d SQL line comments contain `:identifier` patterns that sqlx will parse as required parameters at Exec time:\n%s",
			len(offences), strings.Join(offences, "\n"))
	}
}

// TestSystemFlowFilterMigrationExists sanity-checks the Phase 1
// migration that introduced the flo.system_flow column — without the
// column in place the filter in TestSystemFlowFilterAppliedToAllListQueries
// would be referencing a non-existent field and prepared statement
// compilation would fail at service startup.
func TestSystemFlowFilterMigrationExists(t *testing.T) {
	b, err := os.ReadFile("migration/41_AddAgentMemoryPhase1.up.sql")
	if err != nil {
		t.Fatalf("failed to read Phase 1 migration: %v", err)
	}
	sql := string(b)
	if !strings.Contains(sql, "system_flow BOOLEAN") {
		t.Fatal("Phase 1 migration must add a system_flow BOOLEAN column on flo")
	}
	if !strings.Contains(sql, "DEFAULT FALSE") {
		t.Fatal("system_flow column must default to FALSE so existing rows stay visible")
	}
}

func lineRef(lineNo int, line string) string {
	// Trim leading tabs/spaces for legible test output while still
	// including the original content so the reader knows exactly which
	// clause to fix.
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 120 {
		trimmed = trimmed[:117] + "..."
	}
	return "  service.go:" + itoa(lineNo) + "  " + trimmed
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
