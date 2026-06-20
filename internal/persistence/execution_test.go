package persistence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestMergeDepthCappedFlag_NoExisting verifies that when the parent
// metadata is nil/empty, the helper produces a fresh object with the
// depth_capped sentinel set.
func TestMergeDepthCappedFlag_NoExisting(t *testing.T) {
	RegisterTestingT(t)

	out, err := mergeDepthCappedFlag(nil)
	Expect(err).NotTo(HaveOccurred())

	var parsed map[string]interface{}
	Expect(json.Unmarshal(out, &parsed)).To(Succeed())
	Expect(parsed).To(HaveKeyWithValue("depth_capped", true))
	Expect(parsed).To(HaveLen(1))
}

// TestMergeDepthCappedFlag_PreservesExistingKeys confirms that
// pre-existing parent_metadata keys (plan_id, originating_flow_id…)
// survive the merge. The depth_capped flag is an addition, not a
// replacement.
func TestMergeDepthCappedFlag_PreservesExistingKeys(t *testing.T) {
	RegisterTestingT(t)

	existing := json.RawMessage(`{"plan_id":"plan-1","plan_title":"Quarterly review"}`)
	out, err := mergeDepthCappedFlag(existing)
	Expect(err).NotTo(HaveOccurred())

	var parsed map[string]interface{}
	Expect(json.Unmarshal(out, &parsed)).To(Succeed())
	Expect(parsed).To(HaveKeyWithValue("plan_id", "plan-1"))
	Expect(parsed).To(HaveKeyWithValue("plan_title", "Quarterly review"))
	Expect(parsed).To(HaveKeyWithValue("depth_capped", true))
}

// TestMergeDepthCappedFlag_RejectsInvalidJSON guards against silently
// dropping malformed metadata — if a caller hands us bytes we can't
// parse, surface that so the execution fails loudly rather than
// stripping context the UI was supposed to render.
func TestMergeDepthCappedFlag_RejectsInvalidJSON(t *testing.T) {
	RegisterTestingT(t)

	_, err := mergeDepthCappedFlag(json.RawMessage(`{"not closed`))
	Expect(err).To(HaveOccurred())
}

// TestMaxExecutionDepth_Sane asserts the cap stays small enough that
// the recursive ancestors CTE in commit 3 remains bounded. If someone
// bumps this in future they should think about the downstream queries.
func TestMaxExecutionDepth_Sane(t *testing.T) {
	RegisterTestingT(t)
	Expect(MaxExecutionDepth).To(BeNumerically(">=", 1))
	Expect(MaxExecutionDepth).To(BeNumerically("<=", 50))
}

// TestInsertFloExecutionStatementBindsHierarchyColumns is a text
// guard on stmtInsertFloExecution. Every hierarchy column has to be
// bound or the INSERT will either fail (root_execution_id NOT NULL)
// or silently leave parent linkage empty (parent_*). This catches the
// "added the struct field but forgot the SQL" class of bug at compile
// time.
func TestInsertFloExecutionStatementBindsHierarchyColumns(t *testing.T) {
	RegisterTestingT(t)

	b, err := os.ReadFile("service.go")
	Expect(err).NotTo(HaveOccurred())
	src := string(b)

	// Find the INSERT INTO execution block.
	start := strings.Index(src, "INSERT INTO execution (")
	Expect(start).To(BeNumerically(">=", 0), "stmtInsertFloExecution INSERT block must exist in service.go")
	end := strings.Index(src[start:], ") RETURNING id;")
	Expect(end).To(BeNumerically(">", 0), "INSERT block must terminate with ) RETURNING id;")
	block := src[start : start+end]

	required := []string{
		"id",
		"parent_execution_id",
		"parent_relationship",
		"parent_metadata",
		"root_execution_id",
		"depth",
	}
	for _, col := range required {
		Expect(block).To(ContainSubstring(col),
			"stmtInsertFloExecution must bind the %s column", col)
		Expect(block).To(ContainSubstring(":"+col),
			"stmtInsertFloExecution must bind the :%s named parameter", col)
	}
}
