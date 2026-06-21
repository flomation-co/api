package persistence

// Pure-function tests for the writeback helpers. The transactional
// dispatch path is covered end-to-end at M1 commit 9 + local manual
// validation; what we pin here is the metadata extraction and event-
// name mapping — the two pure functions whose drift would silently
// break the hook.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func TestExtractPlanTaskID_PresentReturnsValue(t *testing.T) {
	RegisterTestingT(t)
	meta := json.RawMessage(`{
		"plan_id":"p-1",
		"plan_task_id":"task-abc",
		"plan_task_name":"ingest"
	}`)
	Expect(extractPlanTaskID(meta)).To(Equal("task-abc"))
}

func TestExtractPlanTaskID_AbsentReturnsEmpty(t *testing.T) {
	RegisterTestingT(t)
	// Execution with parent_metadata that's NOT a plan task — the
	// hook must return "" so the common path (any non-plan execution)
	// no-ops without DB I/O.
	meta := json.RawMessage(`{"agent_id":"a-1","other":"thing"}`)
	Expect(extractPlanTaskID(meta)).To(Equal(""))
}

func TestExtractPlanTaskID_EmptyMetaReturnsEmpty(t *testing.T) {
	RegisterTestingT(t)
	Expect(extractPlanTaskID(nil)).To(Equal(""))
	Expect(extractPlanTaskID(json.RawMessage(""))).To(Equal(""))
}

func TestExtractPlanTaskID_MalformedJSONReturnsEmpty(t *testing.T) {
	// Defensive: corrupt metadata mustn't crash the completion handler.
	RegisterTestingT(t)
	Expect(extractPlanTaskID(json.RawMessage(`{not json`))).To(Equal(""))
}

func TestExtractPlanTaskID_NonStringValueReturnsEmpty(t *testing.T) {
	// plan_task_id must be a string. Anything else (number, object,
	// null) is a malformed payload that the writeback ignores.
	RegisterTestingT(t)
	Expect(extractPlanTaskID(json.RawMessage(`{"plan_task_id":42}`))).To(Equal(""))
	Expect(extractPlanTaskID(json.RawMessage(`{"plan_task_id":null}`))).To(Equal(""))
}

func TestWritebackEventName(t *testing.T) {
	RegisterTestingT(t)
	Expect(writebackEventName(WritebackCompleted)).To(Equal("task_completed"))
	Expect(writebackEventName(WritebackRequeued)).To(Equal("task_retry_queued"))
	Expect(writebackEventName(WritebackFailed)).To(Equal("task_failed"))
	Expect(writebackEventName(WritebackCancelled)).To(Equal("task_cancelled"))
	// None and Idempotent don't emit events — the hook returns the
	// outcome to the HTTP layer but no audit row is appended.
	Expect(writebackEventName(WritebackNone)).To(Equal(""))
	Expect(writebackEventName(WritebackIdempotent)).To(Equal(""))
}

func TestTruncateError_BelowCapPassesThrough(t *testing.T) {
	RegisterTestingT(t)
	short := "boom"
	Expect(truncateError(short)).To(Equal(short))
}

func TestTruncateError_AboveCapTruncates(t *testing.T) {
	RegisterTestingT(t)
	long := strings.Repeat("X", 2100)
	got := truncateError(long)
	Expect(len(got)).To(BeNumerically(">", 2048))
	Expect(got).To(HaveSuffix("… [truncated]"))
}
