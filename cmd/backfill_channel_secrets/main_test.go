package main

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
)

func TestParseChannels(t *testing.T) {
	RegisterTestingT(t)

	out, err := parseChannels(json.RawMessage(`[
		{"type": "slack", "config": {"bot_token": "FAKE_SLACK", "signing_secret": "FAKE_SIG"}},
		{"type": "telegram", "config": {"bot_token": "FAKE_TG"}}
	]`))
	Expect(err).ToNot(HaveOccurred())
	Expect(out).To(HaveLen(2))
	Expect(out[0].Type).To(Equal("slack"))
	Expect(out[0].Config["bot_token"]).To(Equal("FAKE_SLACK"))
	Expect(out[1].Type).To(Equal("telegram"))

	empty, err := parseChannels(json.RawMessage(`[]`))
	Expect(err).ToNot(HaveOccurred())
	Expect(empty).To(BeEmpty())

	null, err := parseChannels(json.RawMessage(`null`))
	Expect(err).ToNot(HaveOccurred())
	Expect(null).To(BeNil())

	none, err := parseChannels(nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(none).To(BeNil())
}

func TestPatchTriggerNodes_existingInput_replaced(t *testing.T) {
	RegisterTestingT(t)

	rev := map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"id": "n-1",
				"data": map[string]interface{}{
					"label": "trigger/slack",
					"config": map[string]interface{}{
						"inputs": []interface{}{
							map[string]interface{}{"name": "bot_token", "value": ""},
							map[string]interface{}{"name": "signing_secret", "value": ""},
						},
					},
				},
			},
		},
	}

	updates := map[string]map[string]string{
		"slack": {
			"bot_token":      "${secrets.slack_bot_token_abc123}",
			"signing_secret": "${secrets.slack_signing_secret_abc123}",
		},
	}

	out, patched, orphans := patchTriggerNodes(rev, updates)
	Expect(patched).To(Equal(1))
	Expect(orphans).To(Equal(0))

	inputs := out["nodes"].([]interface{})[0].(map[string]interface{})["data"].(map[string]interface{})["config"].(map[string]interface{})["inputs"].([]interface{})
	values := map[string]string{}
	for _, i := range inputs {
		m := i.(map[string]interface{})
		values[m["name"].(string)] = m["value"].(string)
	}
	Expect(values["bot_token"]).To(Equal("${secrets.slack_bot_token_abc123}"))
	Expect(values["signing_secret"]).To(Equal("${secrets.slack_signing_secret_abc123}"))
}

func TestPatchTriggerNodes_missingInput_appended(t *testing.T) {
	RegisterTestingT(t)

	rev := map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"id": "n-2",
				"data": map[string]interface{}{
					"label":  "trigger/telegram",
					"config": map[string]interface{}{}, // no inputs key
				},
			},
		},
	}

	updates := map[string]map[string]string{
		"telegram": {"bot_token": "${secrets.telegram_bot_token_def456}"},
	}

	out, patched, _ := patchTriggerNodes(rev, updates)
	Expect(patched).To(Equal(1))

	inputs := out["nodes"].([]interface{})[0].(map[string]interface{})["data"].(map[string]interface{})["config"].(map[string]interface{})["inputs"].([]interface{})
	Expect(inputs).To(HaveLen(1))
	Expect(inputs[0].(map[string]interface{})["name"]).To(Equal("bot_token"))
	Expect(inputs[0].(map[string]interface{})["value"]).To(Equal("${secrets.telegram_bot_token_def456}"))
}

func TestPatchTriggerNodes_nonChannelLabel_skipped(t *testing.T) {
	RegisterTestingT(t)

	rev := map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"id": "n-3",
				"data": map[string]interface{}{
					"label":  "ai/openai",
					"config": map[string]interface{}{"inputs": []interface{}{}},
				},
			},
			map[string]interface{}{
				"id": "n-4",
				"data": map[string]interface{}{
					"label":  "trigger/manual",
					"config": map[string]interface{}{"inputs": []interface{}{}},
				},
			},
		},
	}

	updates := map[string]map[string]string{"slack": {"bot_token": "x"}}

	_, patched, _ := patchTriggerNodes(rev, updates)
	Expect(patched).To(Equal(0), "non-channel nodes must not be patched")
}

func TestPatchTriggerNodes_noMatchingNode(t *testing.T) {
	RegisterTestingT(t)

	rev := map[string]interface{}{"nodes": []interface{}{}}
	updates := map[string]map[string]string{"slack": {"bot_token": "x"}}

	_, patched, _ := patchTriggerNodes(rev, updates)
	Expect(patched).To(Equal(0), "no nodes means nothing to patch")
}

func TestNormaliseRevisionData_acceptsRawMessage(t *testing.T) {
	RegisterTestingT(t)

	raw := json.RawMessage(`{"nodes": [{"id":"x"}]}`)
	out, err := normaliseRevisionData(raw)
	Expect(err).ToNot(HaveOccurred())
	Expect(out["nodes"]).ToNot(BeNil())
}

func TestNormaliseRevisionData_acceptsMap(t *testing.T) {
	RegisterTestingT(t)

	in := map[string]interface{}{"nodes": []interface{}{}}
	out, err := normaliseRevisionData(in)
	Expect(err).ToNot(HaveOccurred())
	Expect(out).To(Equal(in))
}

func TestNormaliseRevisionData_acceptsBytes(t *testing.T) {
	RegisterTestingT(t)

	in := []byte(`{"nodes":[{"id":"y"}]}`)
	out, err := normaliseRevisionData(in)
	Expect(err).ToNot(HaveOccurred())
	Expect(out["nodes"]).ToNot(BeNil())
}
