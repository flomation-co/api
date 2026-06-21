package persistence

// Table-driven tests for SubstituteTaskRefs. Each case is locked in
// per the M1 plan doc — the rule set this helper enforces is the
// contract the executor and the agent's prompt both rely on, so
// changes here should be deliberate.

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
)

func TestSubstituteTaskRefs(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		outputs map[string]map[string]interface{}
		want    string
	}{
		{
			name:  "simple whole-value ref to string",
			input: `{"data":"${pull_metrics.rows}"}`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"rows": "alpha,beta,gamma"},
			},
			want: `{"data":"alpha,beta,gamma"}`,
		},
		{
			name:  "whole-value ref preserves typed number, not stringified",
			input: `{"count":"${pull_metrics.row_count}"}`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"row_count": float64(42)},
			},
			want: `{"count":42}`,
		},
		{
			name:  "whole-value ref preserves typed boolean",
			input: `{"ok":"${verify.passed}"}`,
			outputs: map[string]map[string]interface{}{
				"verify": {"passed": true},
			},
			want: `{"ok":true}`,
		},
		{
			name:  "whole-value ref preserves nested object",
			input: `{"meta":"${pull_metrics.summary}"}`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"summary": map[string]interface{}{"avg": float64(7), "tag": "ok"}},
			},
			want: `{"meta":{"avg":7,"tag":"ok"}}`,
		},
		{
			name:  "partial substitution inside a longer string stringifies",
			input: `{"msg":"received ${pull_metrics.row_count} rows"}`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"row_count": float64(42)},
			},
			want: `{"msg":"received 42 rows"}`,
		},
		{
			name:  "nested objects recurse",
			input: `{"outer":{"inner":"${a.b}"}}`,
			outputs: map[string]map[string]interface{}{
				"a": {"b": "found"},
			},
			want: `{"outer":{"inner":"found"}}`,
		},
		{
			name:  "array of strings each substitutes independently",
			input: `["${pull_metrics.rows}", "${pull_metrics.row_count}", "literal"]`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"rows": "x", "row_count": float64(3)},
			},
			want: `["x",3,"literal"]`,
		},
		{
			name:  "unknown task name preserves ref unchanged",
			input: `{"data":"${ghost.value}"}`,
			outputs: map[string]map[string]interface{}{
				"other_task": {"foo": "bar"},
			},
			want: `{"data":"${ghost.value}"}`,
		},
		{
			name:  "unknown output key on known task preserves ref",
			input: `{"data":"${pull_metrics.missing}"}`,
			outputs: map[string]map[string]interface{}{
				"pull_metrics": {"present": "yes"},
			},
			want: `{"data":"${pull_metrics.missing}"}`,
		},
		{
			name:  "unresolved ref in middle of partial string also preserved",
			input: `{"msg":"got ${ghost.value} rows"}`,
			outputs: map[string]map[string]interface{}{
				"other": {"x": "1"},
			},
			want: `{"msg":"got ${ghost.value} rows"}`,
		},
		{
			name:  "numeric values pass through untouched",
			input: `{"int":1,"float":2.5,"bool":true,"null":null}`,
			outputs: map[string]map[string]interface{}{
				"a": {"b": "wat"},
			},
			want: `{"bool":true,"float":2.5,"int":1,"null":null}`,
		},
		{
			name:  "dotted path traverses nested output map",
			input: `{"city":"${weather.location.name}"}`,
			outputs: map[string]map[string]interface{}{
				"weather": {
					"location": map[string]interface{}{"name": "Berlin"},
				},
			},
			want: `{"city":"Berlin"}`,
		},
		{
			name:  "two refs in same string both substitute",
			input: `{"summary":"task ${a.x} returned ${b.y} items"}`,
			outputs: map[string]map[string]interface{}{
				"a": {"x": "ingest"},
				"b": {"y": float64(7)},
			},
			want: `{"summary":"task ingest returned 7 items"}`,
		},
		{
			name:  "executor namespaces are NOT touched (left for downstream)",
			input: `{"a":"${flow.channel_id}","b":"${user.name}","c":"${secrets.X}","d":"${a.b}"}`,
			outputs: map[string]map[string]interface{}{
				"a": {"b": "ours"},
			},
			// flow./user./secrets. don't have a matching task in
			// outputs, so they pass through verbatim — exactly the
			// behaviour the executor needs to receive them and do its
			// own substitution.
			want: `{"a":"${flow.channel_id}","b":"${user.name}","c":"${secrets.X}","d":"ours"}`,
		},
		{
			name:  "empty inputs returns empty",
			input: ``,
			outputs: map[string]map[string]interface{}{
				"a": {"b": "x"},
			},
			want: ``,
		},
		{
			name:    "empty outputs map leaves everything as refs",
			input:   `{"a":"${task.out}"}`,
			outputs: map[string]map[string]interface{}{},
			want:    `{"a":"${task.out}"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			RegisterTestingT(t)
			got, err := SubstituteTaskRefs(json.RawMessage(tc.input), tc.outputs)
			Expect(err).NotTo(HaveOccurred())
			if tc.want == "" {
				Expect(string(got)).To(Equal(""))
				return
			}
			// Compare via JSON round-trip so map-key ordering doesn't
			// break the assertion (Go's encoding/json sorts keys, but
			// pinning the test to a sorted form keeps drift visible).
			var gotJSON, wantJSON interface{}
			Expect(json.Unmarshal(got, &gotJSON)).To(Succeed())
			Expect(json.Unmarshal([]byte(tc.want), &wantJSON)).To(Succeed())
			Expect(gotJSON).To(Equal(wantJSON))
		})
	}
}

// TestSubstituteTaskRefs_InvalidJSON guards the error path — a
// malformed inputs_json (the column has no shape constraint beyond
// being valid JSONB, but the field could in theory carry garbage) is
// surfaced rather than silently swallowed.
func TestSubstituteTaskRefs_InvalidJSON(t *testing.T) {
	RegisterTestingT(t)
	_, err := SubstituteTaskRefs(json.RawMessage(`{"unterminated":`), nil)
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("decode inputs_json"))
}

// TestStringify_NumberWithoutTrailingZeros verifies the integral-
// float renderer doesn't produce "42.000000" — agents producing row
// counts should see "42" inside templated messages, not a float
// formatted as a string.
func TestStringify_NumberWithoutTrailingZeros(t *testing.T) {
	RegisterTestingT(t)
	Expect(stringify(float64(42))).To(Equal("42"))
	Expect(stringify(float64(3.5))).To(Equal("3.5"))
	Expect(stringify(float64(0))).To(Equal("0"))
	Expect(stringify(true)).To(Equal("true"))
	Expect(stringify(false)).To(Equal("false"))
	Expect(stringify(nil)).To(Equal(""))
	Expect(stringify("plain")).To(Equal("plain"))
	Expect(stringify(map[string]interface{}{"k": "v"})).To(Equal(`{"k":"v"}`))
}
