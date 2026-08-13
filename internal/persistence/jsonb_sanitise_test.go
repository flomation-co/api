package persistence

import (
	"encoding/json"
	"strings"
	"testing"
)

// nulSeq is the six-character escape json.Marshal produces for a NUL byte
// (backslash u 0 0 0 0) — the exact sequence PostgreSQL jsonb rejects.
const nulSeq = "\\u0000"

// TestSanitiseJSONBValue_StripsNUL is the regression guard for the bug where a
// single NUL byte in a flow's node outputs made the whole execution result
// write fail with "pq: unsupported Unicode escape sequence", leaving the
// execution marked success but with a NULL result/completed_at (so the editor
// could render no node state or logs).
func TestSanitiseJSONBValue_StripsNUL(t *testing.T) {
	// A payload whose string value contains the escaped NUL sequence.
	escaped := []byte(`{"tool_result":"ok` + nulSeq + `here","n":1}`)
	// A payload with a raw NUL embedded directly in the bytes.
	raw := []byte("{\"a\":\"x\x00y\"}")

	cases := []struct {
		name string
		in   interface{}
	}{
		{"rawMessage_escaped", json.RawMessage(escaped)},
		{"bytes_escaped", escaped},
		{"string_escaped", string(escaped)},
		{"bytes_rawNUL", raw},
		{"string_rawNUL", string(raw)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SanitiseJSONBValue(tc.in)

			var b []byte
			switch v := out.(type) {
			case json.RawMessage:
				b = v
			case []byte:
				b = v
			case string:
				b = []byte(v)
			default:
				t.Fatalf("unexpected output type %T", out)
			}

			if strings.Contains(string(b), nulSeq) {
				t.Errorf("escaped NUL survived: %q", b)
			}
			if strings.ContainsRune(string(b), 0) {
				t.Errorf("raw NUL survived: %q", b)
			}
			// Output must still be valid JSON — the strip must not corrupt structure.
			var v interface{}
			if err := json.Unmarshal(b, &v); err != nil {
				t.Errorf("sanitised output is not valid JSON: %v (%q)", err, b)
			}
		})
	}
}

// TestSanitiseJSONBValue_CleanPassthrough ensures the common (no-NUL) path
// returns the value unchanged.
func TestSanitiseJSONBValue_CleanPassthrough(t *testing.T) {
	clean := `{"tool_result":"all good","count":42}`
	out := SanitiseJSONBValue(json.RawMessage(clean))
	rm, ok := out.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", out)
	}
	if string(rm) != clean {
		t.Errorf("clean payload was altered: got %q want %q", rm, clean)
	}

	// Non-JSON-bytes types pass through untouched.
	if v := SanitiseJSONBValue(123); v != 123 {
		t.Errorf("int passthrough altered: %v", v)
	}
}

func TestSanitiseJSONBValue_PreservesLiteralBackslashU0000(t *testing.T) {
	// A literal backslash-then-u0000 in the data marshals to two backslashes;
	// the old ReplaceAll stripped the escape from the second one, leaving a
	// lone backslash (invalid JSON). It must be preserved; a genuine NUL must
	// still be stripped.
	in := json.RawMessage(`{"lit":"\\u0000","real":"a\u0000b","n":3}`)
	out := SanitiseJSONBValue(in).(json.RawMessage)
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("literal corrupted to invalid JSON: %v (%q)", err, out)
	}
	if v["lit"] != "\\u0000" {
		t.Errorf("literal value altered: got %q", v["lit"])
	}
	if v["real"] != "ab" {
		t.Errorf("real NUL not stripped: got %q", v["real"])
	}
}
