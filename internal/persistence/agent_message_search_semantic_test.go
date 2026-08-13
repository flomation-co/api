package persistence

import (
	"testing"

	pgvector "github.com/pgvector/pgvector-go"
)

// TestFuseByRRF verifies Reciprocal Rank Fusion: a message ranked well by BOTH
// lists beats one ranked #1 by only a single list, results are deduped by id,
// and the output is truncated to the limit.
func TestFuseByRRF(t *testing.T) {
	mk := func(id string) AgentMessageSearchResult { return AgentMessageSearchResult{ID: id, Content: id} }
	fts := []AgentMessageSearchResult{mk("A"), mk("B"), mk("C")}      // A#0 B#1 C#2
	sem := []AgentMessageSearchResult{mk("B"), mk("D"), mk("A")}      // B#0 D#1 A#2

	out := FuseByRRF([][]AgentMessageSearchResult{fts, sem}, 10)

	// B (top in sem + 2nd in fts) should edge out A (top in fts + 3rd in sem).
	if len(out) != 4 {
		t.Fatalf("expected 4 deduped results, got %d", len(out))
	}
	if out[0].ID != "B" {
		t.Errorf("expected B first (strong in both), got %s", out[0].ID)
	}
	if out[1].ID != "A" {
		t.Errorf("expected A second, got %s", out[1].ID)
	}
	// No duplicates.
	seen := map[string]int{}
	for _, r := range out {
		seen[r.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("id %s appeared %d times (should be deduped)", id, n)
		}
	}

	// Limit truncates.
	if lim := FuseByRRF([][]AgentMessageSearchResult{fts, sem}, 2); len(lim) != 2 {
		t.Errorf("limit=2 should truncate to 2, got %d", len(lim))
	}
}

// TestSearchAgentMessagesByEmbedding_Guards ensures a missing scope short-circuits
// before any DB access (never runs an unscoped vector search).
func TestSearchAgentMessagesByEmbedding_Guards(t *testing.T) {
	var s Service // nil conn: reaching the DB would panic
	v := pgvector.NewVector(make([]float32, 1024))
	if res, err := s.SearchAgentMessagesByEmbedding("", "u", v, 5); res != nil || err != nil {
		t.Errorf("empty agentID: got %v,%v; want nil,nil", res, err)
	}
	if res, err := s.SearchAgentMessagesByEmbedding("a", "", v, 5); res != nil || err != nil {
		t.Errorf("empty userID: got %v,%v; want nil,nil", res, err)
	}
}
