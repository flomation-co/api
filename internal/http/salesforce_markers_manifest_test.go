package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A dynamic-options marker keyed on an action that does not exist, or on an
// input that action does not declare, is SILENTLY INERT: the editor just
// renders a plain text box and nobody finds out until an operator is typing
// Salesforce record IDs by hand. With 429 markers registered programmatically
// from a table, a single typo in an action name or an input rename on the
// executor side is invisible without a check like this.
//
// The manifest is the executor's, not this repo's, so this test can only run
// where both are checked out — a developer's machine, which is exactly where
// the drift gets introduced. It SKIPS rather than fails when the manifest is
// absent (api CI), so it is a local safety net, not a cross-repo build
// dependency. Point SALESFORCE_MANIFEST at the file to override the search.
func TestSalesforceMarkersMatchTheExecutorManifest(t *testing.T) {
	path := os.Getenv("SALESFORCE_MANIFEST")
	if path == "" {
		// Sibling checkout: <root>/api/internal/http -> <root>/executor/...
		wd, err := os.Getwd()
		if err != nil {
			t.Skipf("cannot resolve working directory: %v", err)
		}
		path = filepath.Join(wd, "..", "..", "..", "executor", "internal", "assets", "manifest", "manifest.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("executor manifest not available (%v) — this check only runs alongside an executor checkout", err)
	}

	var manifest map[string]struct {
		Inputs []struct {
			Name string `json:"name"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("cannot parse the executor manifest: %v", err)
	}
	if len(manifest) == 0 {
		t.Skip("executor manifest is empty")
	}

	var badAction, badInput []string
	checked := 0
	for key := range dynamicOptionsMetadata {
		// The poll trigger's picker lives under trigger/, not crm/salesforce/, and
		// is the one Salesforce marker outside that prefix. Checking only the prefix
		// would silently exempt the newest and least-proven marker from the very
		// check that exists to catch a marker naming an input that does not exist.
		if !strings.HasPrefix(key, "crm/salesforce/") && !strings.HasPrefix(key, "trigger/salesforce_") {
			continue
		}
		action, input, ok := strings.Cut(key, "#")
		if !ok {
			t.Errorf("malformed marker key %q — expected actionID#inputName", key)
			continue
		}
		checked++
		def, exists := manifest[action]
		if !exists {
			badAction = append(badAction, key)
			continue
		}
		found := false
		for _, c := range def.Inputs {
			if c.Name == input {
				found = true
				break
			}
		}
		if !found {
			badInput = append(badInput, key)
		}
	}

	sort.Strings(badAction)
	sort.Strings(badInput)
	if len(badAction) > 0 {
		t.Errorf("%d picker marker(s) reference an action that does not exist — these pickers can never render:\n  %s",
			len(badAction), strings.Join(badAction, "\n  "))
	}
	if len(badInput) > 0 {
		t.Errorf("%d picker marker(s) reference an input the action does not declare — these pickers can never render:\n  %s",
			len(badInput), strings.Join(badInput, "\n  "))
	}
	if checked == 0 {
		t.Error("no Salesforce markers were registered at all — the init() in salesforce_options_markers.go is not running")
	}
}
