package persistence

// Phase 7 of the Agent Memory feature — memory hygiene.
//
// Provides the persistence layer for:
//   - Finding contradiction candidates via embedding similarity
//   - Superseding and merging memories
//   - Counting and governing pinned memories
//   - Pin limit enforcement

import (
	"database/sql"
	"errors"
	"fmt"

	api "flomation.app/automate/api"
	pgvector "github.com/pgvector/pgvector-go"
)

// FindContradictionCandidates finds active memories of the same type for
// the same user that are semantically similar to the given embedding.
// Returns candidates ordered by similarity (closest first).
func (s *Service) FindContradictionCandidates(
	agentUserID, memoryType string,
	embedding pgvector.Vector,
	threshold float64,
	limit int,
) ([]*api.AgentMemory, error) {
	var results []*api.AgentMemory
	err := s.conn.Select(&results, `
		SELECT * FROM agent_memory
		WHERE agent_user_id = $1
		  AND memory_type = $2
		  AND status = 'active'
		  AND embedding IS NOT NULL
		  AND 1 - (embedding <=> $3) > $4
		ORDER BY embedding <=> $3
		LIMIT $5
	`, agentUserID, memoryType, embedding, threshold, limit)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// FindNearDuplicates finds active memories with very high embedding
// similarity (near-duplicates). Uses a higher threshold than contradiction
// detection (typically 0.95+).
func (s *Service) FindNearDuplicates(
	agentUserID, memoryType string,
	embedding pgvector.Vector,
	threshold float64,
	excludeID string,
	limit int,
) ([]*api.AgentMemory, error) {
	var results []*api.AgentMemory
	err := s.conn.Select(&results, `
		SELECT *
		FROM agent_memory
		WHERE agent_user_id = $1
		  AND memory_type = $2
		  AND status = 'active'
		  AND embedding IS NOT NULL
		  AND id != $3
		  AND 1 - (embedding <=> $4) > $5
		ORDER BY embedding <=> $4
		LIMIT $6
	`, agentUserID, memoryType, excludeID, embedding, threshold, limit)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// SupersedeMemory marks an old memory as superseded by a new one.
// Automatically unpins the old memory if it was pinned.
func (s *Service) SupersedeMemory(oldID, newID string) error {
	result, err := s.conn.Exec(`
		UPDATE agent_memory
		SET status = 'superseded', superseded_by = $1, pinned = FALSE
		WHERE id = $2 AND status = 'active'
	`, newID, oldID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("memory %s not found or already superseded", oldID)
	}
	return nil
}

// MergeMemory marks a duplicate memory as merged into the canonical one.
func (s *Service) MergeMemory(duplicateID, canonicalID string) error {
	_, err := s.conn.Exec(`
		UPDATE agent_memory
		SET status = 'merged', superseded_by = $1, pinned = FALSE
		WHERE id = $2 AND status = 'active'
	`, canonicalID, duplicateID)
	return err
}

// CountPinnedMemories returns the number of active pinned memories for a user.
func (s *Service) CountPinnedMemories(agentUserID string) (int, error) {
	var count int
	err := s.conn.Get(&count, `
		SELECT COUNT(*) FROM agent_memory
		WHERE agent_user_id = $1 AND pinned = TRUE AND status = 'active'
	`, agentUserID)
	return count, err
}

// UnpinOldestMemories unpins the N oldest pinned active memories for a
// user (by created_at ASC). Returns the IDs of unpinned memories.
func (s *Service) UnpinOldestMemories(agentUserID string, count int) ([]string, error) {
	var ids []string
	err := s.conn.Select(&ids, `
		UPDATE agent_memory
		SET pinned = FALSE
		WHERE id IN (
			SELECT id FROM agent_memory
			WHERE agent_user_id = $1 AND pinned = TRUE AND status = 'active'
			ORDER BY created_at ASC
			LIMIT $2
		)
		RETURNING id
	`, agentUserID, count)
	return ids, err
}

// GetMaxPinnedMemories returns the agent's max_pinned_memories setting.
// Returns 50 as default if not configured.
func (s *Service) GetMaxPinnedMemories(agentID string) (int, error) {
	var result *int
	err := s.conn.Get(&result, `
		SELECT max_pinned_memories FROM agent WHERE id = $1
	`, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 50, nil
		}
		return 50, err
	}
	if result == nil {
		return 50, nil
	}
	return *result, nil
}

// UpdateMaxPinnedMemories sets the max pinned memory limit for an agent.
func (s *Service) UpdateMaxPinnedMemories(agentID string, limit *int) error {
	_, err := s.conn.Exec(`
		UPDATE agent SET max_pinned_memories = $1 WHERE id = $2
	`, limit, agentID)
	return err
}
