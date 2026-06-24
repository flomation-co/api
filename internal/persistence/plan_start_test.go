package persistence

// Pure-function tests for plan_start.go. The transactional SQL
// behaviour is covered end-to-end by the M4 demo runbook.

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestStartOutcomeConstants_StayStable(t *testing.T) {
	// The StartOutcome constants are read by the HTTP layer and by
	// clients via the API response body. Renaming them is a wire-
	// format break.
	RegisterTestingT(t)
	Expect(string(StartOutcomeStarted)).To(Equal("started"))
	Expect(string(StartOutcomeIdempotent)).To(Equal("idempotent"))
	Expect(string(StartOutcomeNotFound)).To(Equal("not_found"))
	Expect(string(StartOutcomeAlreadyTerminal)).To(Equal("already_terminal"))
}
