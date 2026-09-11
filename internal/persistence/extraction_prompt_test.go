package persistence

import (
	"encoding/json"
	"testing"

	"github.com/onsi/gomega"
)

func TestExtractionPromptCarriesTheCurrentTime(t *testing.T) {
	gomega.RegisterTestingT(t)

	gomega.Expect(extractionSystemPrompt).To(gomega.ContainSubstring("${current_time}"),
		"the prompt must interpolate the current time, or the model dates commitments from its training era")
	gomega.Expect(extractionSystemPrompt).To(gomega.ContainSubstring("NEVER emit a \"due_at\" earlier than the current time"))
	gomega.Expect(extractionSystemPrompt).To(gomega.ContainSubstring("copy that phrase VERBATIM into \"due_in\""))
}

func TestExtractionFlowPassesThePromptThrough(t *testing.T) {
	gomega.RegisterTestingT(t)

	data, err := json.Marshal(buildExtractionFlowJSON())
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	gomega.Expect(extractionPromptOf(data)).To(gomega.Equal(extractionSystemPrompt))
}

func TestSupersededPromptsExcludesCurrent(t *testing.T) {
	gomega.RegisterTestingT(t)

	current := promptFingerprint(extractionSystemPrompt)
	for _, hash := range supersededExtractionPrompts {
		gomega.Expect(hash).ToNot(gomega.Equal(current),
			"the shipped prompt is listed as superseded, so every restart would rewrite the revision")
	}
}

func TestSupersededPromptsRecordsTheOneThatShipped(t *testing.T) {
	gomega.RegisterTestingT(t)

	// The prompt that ran on live until 2026-09-10. Recorded so an
	// installation still carrying it is recognised as untouched and
	// upgraded, rather than mistaken for an admin's customisation.
	gomega.Expect(supersededExtractionPrompts).To(gomega.ContainElement(
		"dd80558d2b5500ab7d00635187ce2daf52c697dd7348db2f4ab9fd2bacd95afa"))
}

func TestExtractionPromptOfIgnoresAnUnrecognisableRevision(t *testing.T) {
	gomega.RegisterTestingT(t)

	gomega.Expect(extractionPromptOf([]byte(`not json`))).To(gomega.BeEmpty())
	gomega.Expect(extractionPromptOf([]byte(`{"nodes":[]}`))).To(gomega.BeEmpty())
	gomega.Expect(extractionPromptOf([]byte(`{"nodes":[{"data":{"label":"ai/anthropic","config":{"inputs":[{"name":"prompt","value":"x"}]}}}]}`))).To(gomega.BeEmpty())
}
