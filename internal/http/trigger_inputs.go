package http

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// TriggerInputOption is a single selectable option for a dropdown-typed
// manual trigger input.
type TriggerInputOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// TriggerInput describes one typed input a manual trigger declares. The
// shape mirrors the editor's Trigger Inputs builder and MUST stay in
// lock-step with the Launch service's equivalent contract — both
// validate the same payloads.
type TriggerInput struct {
	Name        string               `json:"name"`
	Label       string               `json:"label"`
	Type        string               `json:"type"`
	Required    bool                 `json:"required"`
	Placeholder string               `json:"placeholder"`
	Value       string               `json:"value"`
	Options     []TriggerInputOption `json:"options"`
}

// ValidateTriggerInputs checks a submitted payload against a manual
// trigger's declared input schema and returns the names of any offending
// fields. A field is offending when it is required but missing/empty, or
// when a supplied value fails its declared type. A non-required field
// left empty is fine. An empty return slice means the payload is valid.
//
// The function is pure (no I/O, no shared state) so it is trivially
// unit-testable and shares no state with the request.
func ValidateTriggerInputs(schema []TriggerInput, data map[string]interface{}) []string {
	var offending []string

	for _, in := range schema {
		if in.Name == "" {
			// A schema entry without a name can never be satisfied or
			// violated — skip it rather than reject every payload.
			continue
		}

		raw, present := data[in.Name]
		if !present || isEmptyTriggerValue(raw) {
			if in.Required {
				offending = append(offending, in.Name)
			}
			// Non-required empty values are always acceptable.
			continue
		}

		if !triggerValueMatchesType(in, raw) {
			offending = append(offending, in.Name)
		}
	}

	return offending
}

// isEmptyTriggerValue reports whether a submitted value counts as empty.
// Only nil and whitespace-only strings are considered empty — a numeric
// 0 or boolean false is a legitimate value.
func isEmptyTriggerValue(v interface{}) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// triggerValueMatchesType reports whether a present (non-empty) value
// satisfies its declared input type. Unknown/text/string types accept
// any value.
func triggerValueMatchesType(in TriggerInput, v interface{}) bool {
	switch in.Type {
	case "integer":
		return triggerValueIsNumber(v)
	case "boolean":
		return triggerValueIsBoolean(v)
	case "date":
		return triggerValueIsDate(v)
	case "dropdown":
		return triggerValueInOptions(in.Options, v)
	default:
		// "string", "text", empty, or any unrecognised type — accept as-is.
		return true
	}
}

// triggerValueIsNumber accepts native JSON numbers and numeric strings.
func triggerValueIsNumber(v interface{}) bool {
	switch t := v.(type) {
	case float64:
		return true
	case float32:
		return true
	case int, int8, int16, int32, int64:
		return true
	case json.Number:
		_, err := t.Float64()
		return err == nil
	case string:
		_, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return err == nil
	default:
		return false
	}
}

// triggerValueIsBoolean accepts a native bool or the strings "true"/"false".
func triggerValueIsBoolean(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return true
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "false"
	default:
		return false
	}
}

// triggerValueIsDate accepts a value parseable as YYYY-MM-DD or RFC3339.
func triggerValueIsDate(v interface{}) bool {
	s, ok := triggerValueAsString(v)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	return false
}

// triggerValueInOptions reports whether the value matches one of the
// dropdown's allowed option values.
func triggerValueInOptions(options []TriggerInputOption, v interface{}) bool {
	s, ok := triggerValueAsString(v)
	if !ok {
		return false
	}
	for _, opt := range options {
		if opt.Value == s {
			return true
		}
	}
	return false
}

// triggerValueAsString coerces a scalar submitted value to a string for
// comparison. Non-scalar values (maps, slices) are rejected.
func triggerValueAsString(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// manualTriggerRegistrationData builds the payload registered with the
// Launch service for a manual trigger. Launch reads trigger_inputs to
// validate inbound submissions and run_token to enforce auth; __node_id
// tells the executor which node is the entry point (mirroring how the
// other trigger types are registered).
func manualTriggerRegistrationData(nodeID string, triggerInputs []map[string]interface{}, runToken string) map[string]interface{} {
	if triggerInputs == nil {
		triggerInputs = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"trigger_inputs": triggerInputs,
		"run_token":      runToken,
		"__node_id":      nodeID,
	}
}

// extractManualTriggerInputs parses a flow revision's data blob and
// returns the trigger_inputs schema declared on its manual trigger node
// (if any). The bool result distinguishes "no manual node found" from
// "manual node with an empty schema".
func extractManualTriggerInputs(data interface{}) ([]TriggerInput, bool) {
	raw, ok := revisionDataToBytes(data)
	if !ok {
		return nil, false
	}

	var doc struct {
		Nodes []struct {
			Type string `json:"type"`
			Data struct {
				Label  string `json:"label"`
				Config struct {
					TriggerInputs []TriggerInput `json:"trigger_inputs"`
				} `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}

	for _, n := range doc.Nodes {
		if n.Type == "trigger/manual" || n.Data.Label == "trigger/manual" {
			return n.Data.Config.TriggerInputs, true
		}
	}
	return nil, false
}

// revisionDataToBytes normalises a revision's Data (which may arrive as
// raw JSON bytes from the database or as an already-decoded value) to a
// byte slice for re-parsing.
func revisionDataToBytes(data interface{}) ([]byte, bool) {
	switch v := data.(type) {
	case nil:
		return nil, false
	case []byte:
		return v, true
	case json.RawMessage:
		return v, true
	case string:
		return []byte(v), true
	default:
		b, err := json.Marshal(data)
		if err != nil {
			return nil, false
		}
		return b, true
	}
}
