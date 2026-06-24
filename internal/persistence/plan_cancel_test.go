package persistence

// Pure-function tests for plan_cancel.go. The transactional SQL
// behaviour is covered end-to-end by the M3 demo runbook (real
// Postgres). What we pin here is the outcome enum stability and
// the truncateReason helper's behaviour.

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func TestCancelOutcomeConstants_StayStable(t *testing.T) {
	// The CancelOutcome constants are read by the HTTP layer
	// (via service.CancelPlan return) and by clients via the
	// API response body's `outcome` field. Renaming them is a
	// wire-format break. Pin them.
	RegisterTestingT(t)
	Expect(string(CancelOutcomeCancelled)).To(Equal("cancelled"))
	Expect(string(CancelOutcomeNotFound)).To(Equal("not_found"))
	Expect(string(CancelOutcomeIdempotent)).To(Equal("idempotent"))
}

func TestTruncateReason_BelowCapPassesThrough(t *testing.T) {
	RegisterTestingT(t)
	Expect(truncateReason("user changed their mind")).To(Equal("user changed their mind"))
}

func TestTruncateReason_EmptyPassesThrough(t *testing.T) {
	// Empty reason is a valid input — the confirmation dialog's
	// reason textarea is optional. We store the empty string
	// rather than NULL because the SQL column is NOT NULL when
	// cancelled_reason is supplied. Defensive: confirm we don't
	// inject any suffix on empty input.
	RegisterTestingT(t)
	Expect(truncateReason("")).To(Equal(""))
}

func TestTruncateReason_AboveCapTruncates(t *testing.T) {
	RegisterTestingT(t)
	long := strings.Repeat("X", 2100)
	got := truncateReason(long)
	Expect(len(got)).To(BeNumerically(">", 2048))
	Expect(got).To(HaveSuffix("… [truncated]"))
}

func TestTruncateReason_ExactlyAtCapPassesThrough(t *testing.T) {
	// Boundary case: exactly 2048 characters should NOT be
	// truncated. Off-by-one regressions are the easy way to lose
	// the last character of a legitimate reason.
	RegisterTestingT(t)
	atCap := strings.Repeat("X", 2048)
	Expect(truncateReason(atCap)).To(Equal(atCap))
}
