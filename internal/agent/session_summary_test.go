package agent

import (
	"testing"

	api "flomation.app/automate/api"

	. "github.com/onsi/gomega"
)

// The prompt shape is the contract between two code paths (inbound and
// the sweeper poller) and the extraction flow that special-cases
// role="summary". Keep this test in lock-step with
// extraction_bootstrap.go's role=summary instructions — if the prompt
// drifts, the extraction model stops producing session_summary memories.

func TestBuildSessionSummaryPrompt_EmptyTranscript(t *testing.T) {
	RegisterTestingT(t)

	Expect(BuildSessionSummaryPrompt(nil)).To(BeEmpty())
	Expect(BuildSessionSummaryPrompt([]*api.AgentMessage{})).To(BeEmpty())
}

func TestBuildSessionSummaryPrompt_IncludesInstructionsAndTranscript(t *testing.T) {
	RegisterTestingT(t)

	msgs := []*api.AgentMessage{
		{Direction: "inbound", Content: "what's the weather"},
		{Direction: "outbound", Content: "sunny"},
	}

	prompt := BuildSessionSummaryPrompt(msgs)

	Expect(prompt).To(ContainSubstring("Summarise this completed conversation"))
	Expect(prompt).To(ContainSubstring("2-3 sentences"))
	Expect(prompt).To(ContainSubstring("factual summary, not as a message"))
	Expect(prompt).To(ContainSubstring("[user]: what's the weather"))
	Expect(prompt).To(ContainSubstring("[assistant]: sunny"))
}

func TestBuildSessionSummaryPrompt_RoleLabels(t *testing.T) {
	RegisterTestingT(t)

	// Anything other than "outbound" labels as "user" — the in-band path
	// and the sweeper rely on the same fallback so e.g. a "system" or
	// "summary" Direction doesn't accidentally become an assistant turn.
	msgs := []*api.AgentMessage{
		{Direction: "inbound", Content: "one"},
		{Direction: "outbound", Content: "two"},
		{Direction: "system", Content: "three"},
	}

	prompt := BuildSessionSummaryPrompt(msgs)

	Expect(prompt).To(ContainSubstring("[user]: one"))
	Expect(prompt).To(ContainSubstring("[assistant]: two"))
	Expect(prompt).To(ContainSubstring("[user]: three"))
}
