package persistence

// Phase 4 of the Agent Memory feature — semantic retrieval with pgvector.
//
// This file adds three capabilities:
//
//   - SearchMemoriesByEmbedding: cosine-similarity search over the
//     embedding column added in migration 48.
//   - GetMemoriesWithoutEmbedding: used by the Launch backfill goroutine
//     to find memories that need embeddings generated.
//   - UpdateMemoryEmbedding: patches the embedding column on an existing
//     memory row.
//
// See plans/agent_memory.md §"Phase 4 — Semantic retrieval with embeddings".

import (
	"database/sql"
	"errors"

	"flomation.app/automate/api"
	pgvector "github.com/pgvector/pgvector-go"
)

// SearchMemoriesByEmbedding performs a cosine-similarity search over the
// agent_memory table's HNSW index, returning the top-K most relevant
// memories for a given user. When excludePinned is true, pinned memories
// are filtered out (the caller already includes them separately in the
// system prompt).
func (s *Service) SearchMemoriesByEmbedding(
	agentID string,
	agentUserID string,
	embedding pgvector.Vector,
	topK int,
	excludePinned bool,
) ([]*api.AgentMemory, error) {
	if topK <= 0 {
		topK = 10
	}
	if topK > 100 {
		topK = 100
	}

	var results []*api.AgentMemory
	if err := s.stmtSearchMemoriesByEmbedding.Select(&results, struct {
		AgentID       string           `db:"agent_id"`
		AgentUserID   string           `db:"agent_user_id"`
		QueryEmbed    pgvector.Vector  `db:"query_embedding"`
		TopK          int              `db:"top_k"`
		ExcludePinned bool             `db:"exclude_pinned"`
	}{
		AgentID:       agentID,
		AgentUserID:   agentUserID,
		QueryEmbed:    embedding,
		TopK:          topK,
		ExcludePinned: excludePinned,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return results, nil
}

// GetMemoriesWithoutEmbedding returns memories that have no embedding
// vector, ordered by most recent first. Used by Launch's background
// backfill goroutine.
func (s *Service) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var results []*api.AgentMemory
	if err := s.stmtGetMemoriesWithoutEmbedding.Select(&results, struct {
		Limit int `db:"limit"`
	}{Limit: limit}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return results, nil
}

// UpdateMemoryEmbedding sets the embedding vector on an existing memory
// row. Returns an error if the row does not exist.
func (s *Service) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	result, err := s.stmtUpdateMemoryEmbedding.Exec(struct {
		ID        string          `db:"id"`
		Embedding pgvector.Vector `db:"embedding"`
	}{
		ID:        id,
		Embedding: embedding,
	})
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}