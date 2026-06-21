package persistence

// Variable substitution for plan_task inputs — the templating that
// lets one task consume another's outputs at dispatch time. See
// plans/agent_planning.md §"Variable substitution" for the model.
//
// Scope: this helper handles ONLY plan-scoped task refs of the form
// ${<task_name>.<output_name>[.<sub_key>...]}. Cross-cutting variable
// namespaces — ${flow.X}, ${user.X}, ${secrets.X}, ${var.X}, etc. —
// are handled by the executor's standard substitution at execution
// time. We're a thin pre-flight pass: just patch in the values that
// only exist at plan-walk time.
//
// Why TWO substitution passes rather than one?
//
// The plan tick happens in the API (where the plan and its task
// outputs live in the DB). The executor never sees plan structure —
// it sees a trigger payload. Substituting task refs in the API means
// the executor's trigger data carries already-resolved values for
// cross-task references; the executor's own substitution then handles
// the namespaces it owns. Cleanly split responsibilities.
//
// Whole-value vs partial substitution
//
// A string that IS a single ref (`"${pull_metrics.row_count}"`) is
// replaced with the OUTPUT'S TYPED VALUE — an integer stays an
// integer, an object stays an object. A string that CONTAINS a ref
// (`"got ${pull_metrics.row_count} rows"`) gets the ref expanded
// inline and stringified into the surrounding text. This matches the
// executor's own substitution semantics so flow authors don't have to
// hold two mental models.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// taskRefPattern matches one ${name.output[.sub.sub...]} reference.
// Capture groups: 1=task_name, 2=path-after-the-dot (output[.sub...]).
// Task and output names follow the same identifier shape (word chars
// plus dash/underscore) to keep the parser tight.
var taskRefPattern = regexp.MustCompile(`\$\{([a-zA-Z_][\w-]*)\.([a-zA-Z_][\w\-.]*)\}`)

// wholeRefPattern is the anchored form — a string that IS a single
// ref and nothing else. Used to decide between "preserve typed value"
// and "inline-stringify into surrounding text".
var wholeRefPattern = regexp.MustCompile(`^\$\{([a-zA-Z_][\w-]*)\.([a-zA-Z_][\w\-.]*)\}$`)

// TaskRefNamesIn extracts the unique task names referenced by any
// ${name.X} occurrence in the supplied text. Returned slice is
// stable-sorted ascending. Used by plan/create's structural
// validation to detect forward references that wouldn't resolve at
// substitution time.
func TaskRefNamesIn(text string) []string {
	matches := taskRefPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	// Stable order for deterministic test output.
	sortStrings(out)
	return out
}

// sortStrings is a tiny inline sort to avoid bringing in the sort
// package just for this helper. Stable, ascending, O(N^2) — fine
// given task counts are small.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// SubstituteTaskRefs walks the supplied JSONB recursively, replacing
// ${<task_name>.<output_name>[.<sub>...]} references against the
// provided outputs map. Refs that don't resolve are left unchanged —
// the executor's standard substitution gets the next shot at them.
//
// The taskOutputs map is keyed by task name (the human-readable
// `plan_task.name`, not the UUID) and holds whatever the task's
// completion writeback stored in plan_task.outputs_json.
//
// Returns a fresh json.RawMessage; the input is never mutated.
func SubstituteTaskRefs(
	inputs json.RawMessage,
	taskOutputs map[string]map[string]interface{},
) (json.RawMessage, error) {
	if len(inputs) == 0 {
		return inputs, nil
	}
	var decoded interface{}
	if err := json.Unmarshal(inputs, &decoded); err != nil {
		return nil, fmt.Errorf("decode inputs_json: %w", err)
	}
	walked := walkAndSubstitute(decoded, taskOutputs)
	out, err := json.Marshal(walked)
	if err != nil {
		return nil, fmt.Errorf("re-encode inputs_json: %w", err)
	}
	return out, nil
}

// walkAndSubstitute recurses through any JSON-shaped Go value
// (map[string]interface{}, []interface{}, string, number, bool, nil)
// and applies the substitution rule to string leaves.
func walkAndSubstitute(v interface{}, outputs map[string]map[string]interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return substituteString(t, outputs)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, child := range t {
			out[k] = walkAndSubstitute(child, outputs)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, child := range t {
			out[i] = walkAndSubstitute(child, outputs)
		}
		return out
	default:
		// Numbers, booleans, nil — pass through untouched.
		return v
	}
}

// substituteString handles the two cases (whole-value vs partial) for
// a single string leaf. Failed resolutions return the input string
// unchanged so the executor can have another go.
func substituteString(s string, outputs map[string]map[string]interface{}) interface{} {
	// Whole-value case first: if the entire string is one ref, we can
	// preserve typing.
	if match := wholeRefPattern.FindStringSubmatch(s); match != nil {
		resolved, ok := lookup(outputs, match[1], match[2])
		if !ok {
			return s
		}
		return resolved
	}

	// Partial substitution: find each ref, stringify the resolution.
	// ReplaceAllStringFunc avoids re-running the regex on substituted
	// text, so an output value that happens to look like a ref doesn't
	// trigger a second-pass expansion.
	return taskRefPattern.ReplaceAllStringFunc(s, func(ref string) string {
		m := taskRefPattern.FindStringSubmatch(ref)
		if m == nil {
			return ref
		}
		resolved, ok := lookup(outputs, m[1], m[2])
		if !ok {
			return ref
		}
		return stringify(resolved)
	})
}

// lookup walks the dot-separated path after the task name through
// the task's outputs. The first segment matches a key on the
// task's outputs map; subsequent segments traverse nested maps.
// Array indexing isn't supported in M1 — if a flow author needs to
// pick out array[0], they can wrap a flow around it. Keeps M1's
// surface tight.
func lookup(outputs map[string]map[string]interface{}, taskName, path string) (interface{}, bool) {
	taskOut, ok := outputs[taskName]
	if !ok || taskOut == nil {
		return nil, false
	}
	segments := strings.Split(path, ".")
	var cursor interface{} = taskOut
	for _, seg := range segments {
		m, ok := cursor.(map[string]interface{})
		if !ok {
			return nil, false
		}
		next, ok := m[seg]
		if !ok {
			return nil, false
		}
		cursor = next
	}
	return cursor, true
}

// stringify renders a resolved value to its in-text form for partial
// substitution. Numbers, booleans, and strings get their direct
// textual form; complex types fall back to JSON encoding so the
// caller never sees Go's default %v noise inside a templated string.
func stringify(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	case float64:
		// JSON numbers come back as float64. If the value is integral
		// we render it without a trailing ".0" — agents producing
		// row counts shouldn't see "42.000000".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
