package persistence

import (
	"encoding/json"
	"strconv"
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

	data, err := json.Marshal(buildExtractionFlowJSON(extractionSystemPrompt))
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

func TestExtractionFlowRoutesEveryProvider(t *testing.T) {
	gomega.RegisterTestingT(t)

	data, err := json.Marshal(buildExtractionFlowJSON(extractionSystemPrompt))
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	var revision struct {
		Nodes []struct {
			ID   string `json:"id"`
			Data struct {
				Label string `json:"label"`
			} `json:"data"`
		} `json:"nodes"`
		Edges []struct {
			Source       string `json:"source"`
			Target       string `json:"target"`
			SourceHandle string `json:"sourceHandle"`
		} `json:"edges"`
	}
	gomega.Expect(json.Unmarshal(data, &revision)).To(gomega.Succeed())

	var switchID, processID string
	byID := map[string]string{}
	for _, node := range revision.Nodes {
		byID[node.ID] = node.Data.Label
		switch node.Data.Label {
		case "conditional/switch":
			switchID = node.ID
		case "agent/process_extraction":
			processID = node.ID
		}
	}
	gomega.Expect(switchID).ToNot(gomega.BeEmpty())
	gomega.Expect(processID).ToNot(gomega.BeEmpty())

	// Every provider must be reachable from its own switch handle and
	// must feed the process node. A provider wired to the wrong handle
	// would silently extract with someone else's model.
	for i, provider := range extractionProviders {
		label := "ai/" + provider.Name
		handle := "case_" + strconv.Itoa(i)

		var providerID string
		for id, l := range byID {
			if l == label {
				providerID = id
			}
		}
		gomega.Expect(providerID).ToNot(gomega.BeEmpty(), "no node for %s", label)

		in, out := false, false
		for _, edge := range revision.Edges {
			if edge.Source == switchID && edge.Target == providerID && edge.SourceHandle == handle {
				in = true
			}
			if edge.Source == providerID && edge.Target == processID {
				out = true
			}
		}
		gomega.Expect(in).To(gomega.BeTrue(), "%s is not wired to %s", label, handle)
		gomega.Expect(out).To(gomega.BeTrue(), "%s does not feed the process node", label)
	}
}

func TestExtractionFlowIsCurrentDetectsAnOlderShape(t *testing.T) {
	gomega.RegisterTestingT(t)

	current, err := json.Marshal(buildExtractionFlowJSON(extractionSystemPrompt))
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(extractionFlowIsCurrent(current, extractionSystemPrompt)).To(gomega.BeTrue())

	// A customised prompt is current only against that same prompt, so
	// an install that edited the prompt is not rewritten every restart.
	gomega.Expect(extractionFlowIsCurrent(current, "something else")).To(gomega.BeFalse())

	// The single-provider shape this replaced must read as out of date,
	// or an existing install never gains provider routing.
	legacy := []byte(`{"nodes":[
		{"data":{"label":"manual","config":{"inputs":[]}}},
		{"data":{"label":"ai/anthropic","config":{"inputs":[{"name":"system_prompt","value":"p"},{"name":"model","value":"claude-haiku-4-5-20251001"}]}}},
		{"data":{"label":"agent/process_extraction","config":{"inputs":[]}}}
	]}`)
	gomega.Expect(extractionFlowIsCurrent(legacy, "p")).To(gomega.BeFalse())
	gomega.Expect(extractionFlowIsOurs(legacy)).To(gomega.BeTrue())
}

func TestExtractionFlowIsOursRejectsAnAddedNode(t *testing.T) {
	gomega.RegisterTestingT(t)

	edited := []byte(`{"nodes":[
		{"data":{"label":"manual","config":{"inputs":[]}}},
		{"data":{"label":"ai/anthropic","config":{"inputs":[]}}},
		{"data":{"label":"slack/send_message","config":{"inputs":[]}}},
		{"data":{"label":"agent/process_extraction","config":{"inputs":[]}}}
	]}`)
	gomega.Expect(extractionFlowIsOurs(edited)).To(gomega.BeFalse())
	gomega.Expect(extractionFlowIsOurs([]byte(`{"nodes":[]}`))).To(gomega.BeFalse())
	gomega.Expect(extractionFlowIsOurs([]byte(`nonsense`))).To(gomega.BeFalse())
}
