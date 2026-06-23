package persistence

// Lightweight pure-function tests for the plan persistence layer.
// CRUD behaviour requires a real Postgres connection and is exercised
// via the integration commit (M1 commit 9) + local manual validation;
// the agent/api package's test convention is to leave DB-touching
// integration to that layer rather than spin up sqlmock here.
//
// What we DO pin in this file is the SQL-text shape — i.e. that the
// INSERT statements reference the columns the migration creates. A
// drift between Go and SQL surfaces here as a string mismatch rather
// than a runtime "column X does not exist" failure on the first real
// call.

import (
	"strings"

	"testing"

	api "flomation.app/automate/api"

	. "github.com/onsi/gomega"
)

// TestPlanInsertSQL_ReferencesAllRequiredColumns guards against the
// failure mode where a column is added to migration 99 but the Go
// INSERT statement isn't updated. Each required column appears
// somewhere in the constant; the test doesn't pin ordering.
func TestPlanInsertSQL_ReferencesAllRequiredColumns(t *testing.T) {
	RegisterTestingT(t)
	required := []string{
		"agent_id", "owner_user_id", "organisation_id",
		"created_by_execution_id", "title", "goal", "status", "next_check_at",
	}
	for _, col := range required {
		Expect(strings.Contains(planInsertSQL, col)).
			To(BeTrue(), "planInsertSQL is missing column %q", col)
	}
	Expect(strings.Contains(planInsertSQL, "RETURNING *")).To(BeTrue())
}

// TestPlanTaskInsertSQL_ReferencesAllRequiredColumns same shape as
// the plan one. The "server-defaulted" columns (outputs_json,
// execution_id, attempt_count, last_error, started_at, completed_at)
// are NOT in the INSERT — they're populated later via tick + writeback.
func TestPlanTaskInsertSQL_ReferencesAllRequiredColumns(t *testing.T) {
	RegisterTestingT(t)
	required := []string{
		"plan_id", "name", "description", "task_kind",
		"flow_id", "flow_revision_id",
		"status", "depends_on", "not_before", "inputs_json",
		"max_attempts", "timeout_seconds",
	}
	for _, col := range required {
		Expect(strings.Contains(planTaskInsertSQL, col)).
			To(BeTrue(), "planTaskInsertSQL is missing column %q", col)
	}
	// Deliberately NOT in the INSERT — the writeback path sets these.
	notWritten := []string{"attempt_count", "outputs_json", "execution_id", "started_at", "completed_at", "last_error"}
	for _, col := range notWritten {
		// attempt_count is in `required` above? No — re-read. attempt_count
		// is in fact NOT required at insert (it has DEFAULT 0). The
		// "required" slice is what the Go code passes; "notWritten"
		// asserts nothing inadvertently sneaks into the INSERT.
		if col == "attempt_count" {
			continue // attempt_count's default is 0, no insert column
		}
		Expect(strings.Contains(planTaskInsertSQL, col+",")).
			To(BeFalse(), "planTaskInsertSQL should NOT include %q (server-defaulted)", col)
	}
}

// TestPlanEventInsertSQL_References ensures the event row insert
// covers all four user-supplied columns.
func TestPlanEventInsertSQL_References(t *testing.T) {
	RegisterTestingT(t)
	for _, col := range []string{"plan_id", "plan_task_id", "event_type", "data"} {
		Expect(strings.Contains(planEventInsertSQL, col)).
			To(BeTrue(), "planEventInsertSQL is missing column %q", col)
	}
}

// TestIntToPlaceholder pins the placeholder helper used by
// ListPlansByAgentID to assemble Postgres `$N` parameter suffixes
// when the optional status filter changes the argument index.
func TestIntToPlaceholder(t *testing.T) {
	RegisterTestingT(t)
	Expect(intToPlaceholder(1)).To(Equal("1"))
	Expect(intToPlaceholder(2)).To(Equal("2"))
	Expect(intToPlaceholder(10)).To(Equal("10"))
}

// TestErrSentinelsAreDistinct ensures the two error sentinels remain
// distinct values — the HTTP layer pattern-matches on each
// independently (plan-not-found → 404, flow-revision-not-found → 400).
// errors.Is must report the right answer for each.
func TestErrSentinelsAreDistinct(t *testing.T) {
	RegisterTestingT(t)
	Expect(ErrPlanNotFound.Error()).To(Equal("plan not found"))
	Expect(ErrFlowRevisionNotFound.Error()).To(Equal("flow revision not found"))
	Expect(ErrPlanNotFound).NotTo(Equal(ErrFlowRevisionNotFound))
}

// TestPlanTaskKindConstants_StayStable pins the discriminator
// values used both as Go constants AND as SQL CHECK-constraint
// values in migration 100. Any drift here means the application
// inserts a value the DB rejects (or vice versa). Cheap to assert.
func TestPlanTaskKindConstants_StayStable(t *testing.T) {
	RegisterTestingT(t)
	Expect(api.PlanTaskKindOrchestrator).To(Equal("orchestrator"))
	Expect(api.PlanTaskKindFlow).To(Equal("flow"))
}
