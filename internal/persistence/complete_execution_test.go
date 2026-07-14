package persistence

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestCompleteExecutionSQL_SetsEveryCompletionColumn pins the shape of the
// single-statement completion write. The whole point of CompleteExecution is
// that status, completion status and result land ATOMICALLY (a /wait long-poll
// wakes on execution_status='executed', so a result written in a later statement
// could be read as NULL). If a column silently drops out of this UPDATE, or the
// text is split into more than one statement, that atomicity is lost — this test
// fails loudly rather than at runtime.
func TestCompleteExecutionSQL_SetsEveryCompletionColumn(t *testing.T) {
	RegisterTestingT(t)

	// All three completion columns plus both timestamps are written together.
	for _, col := range []string{
		"execution_status = :execution_status",
		"completion_status = :completion_status",
		"result = :result",
		"updated_at = CURRENT_TIMESTAMP",
		"completed_at = CURRENT_TIMESTAMP",
	} {
		Expect(strings.Contains(completeExecutionSQL, col)).
			To(BeTrue(), "completeExecutionSQL is missing %q", col)
	}

	// Scoped to a single row by id.
	Expect(strings.Contains(completeExecutionSQL, "id = :id")).To(BeTrue())

	// It must be ONE statement — a second UPDATE would reintroduce the very
	// non-atomic multi-round-trip window this method exists to remove. (Match
	// "UPDATE execution" rather than the bare keyword so "updated_at" doesn't
	// count as a second statement.)
	Expect(strings.Count(completeExecutionSQL, "UPDATE execution")).
		To(Equal(1), "completeExecutionSQL must be a single UPDATE statement")
	Expect(strings.Count(completeExecutionSQL, ";")).
		To(Equal(1), "completeExecutionSQL must contain exactly one statement terminator")
}
