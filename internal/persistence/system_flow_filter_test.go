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

	lines := strings.Split(src, "\n")
	var missing []string
	found := 0
	for i, line := range lines {
		if !strings.Contains(line, archivedMarker) {
			continue
		}
		found++
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
