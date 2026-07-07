package http

import "testing"

func TestTrelloNamedOptionsSortsAndFallsBack(t *testing.T) {
	body := []byte(`[{"id":"b2","name":"Zeta"},{"id":"b1","name":"alpha"},{"id":"b3","name":""},{"id":"","name":"skip"}]`)
	opts, errMsg := trelloNamedOptions(body)
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	// The blank-id row is dropped; the empty-name row falls back to its id.
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d (%+v)", len(opts), opts)
	}
	// Sorted case-insensitively: alpha, b3 (id fallback), Zeta.
	if opts[0].Name != "alpha" || opts[1].Name != "b3" || opts[2].Name != "Zeta" {
		t.Fatalf("unexpected order/labels: %+v", opts)
	}
	if opts[1].Value != "b3" {
		t.Fatalf("empty-name option should keep its id as value, got %q", opts[1].Value)
	}
}

func TestTrelloNamedOptionsRejectsNonArray(t *testing.T) {
	if _, errMsg := trelloNamedOptions([]byte(`{"error":"nope"}`)); errMsg == "" {
		t.Fatal("expected a parse error for a non-array body")
	}
}
