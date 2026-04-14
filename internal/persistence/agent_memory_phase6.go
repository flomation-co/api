package persistence

// Phase 6 of the Agent Memory feature — user-visible memory management,
// retention policies, and audit logging.
//
// Provides the persistence layer for:
//   - Mapping authenticated Sentinel users to agent_user records
//   - Updating and bulk-deleting memories
//   - Retention-based memory expiry
//   - Audit log creation and querying
//   - Identity unlinking
//   - Data export aggregation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	api "flomation.app/automate/api"
)

// GetAgentUserByEmail finds an agent_user by matching an email address
// against the agent_identity table. This is the bridge between a
// JWT-authenticated Sentinel user (who has an email) and their agent_user
// record (which was created when they first interacted via a channel).
func (s *Service) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) {
	var result api.AgentUser
	err := s.conn.Get(&result, `
		SELECT au.* FROM agent_user au
		JOIN agent_identity ai ON ai.agent_user_id = au.id
		WHERE au.agent_id = $1
		  AND ai.channel_external_id = $2
		LIMIT 1
	`, agentID, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// GetAgentUsersByAgentID returns all users for an agent with pagination.
func (s *Service) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) {
	var results []*api.AgentUser
	err := s.conn.Select(&results, `
		SELECT * FROM agent_user
		WHERE agent_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, agentID, limit, offset)
	return results, err
}

// UpdateAgentMemory updates the title, body, and pinned status of a memory.
func (s *Service) UpdateAgentMemory(id, title, body string, pinned bool) error {
	_, err := s.conn.Exec(`
		UPDATE agent_memory
		SET title = $1, body = $2, pinned = $3
		WHERE id = $4
	`, title, body, pinned, id)
	return err
}

// DeleteAllMemoriesForUser removes all memories for a specific agent_user.
func (s *Service) DeleteAllMemoriesForUser(agentUserID string) (int64, error) {
	result, err := s.conn.Exec(`
		DELETE FROM agent_memory WHERE agent_user_id = $1
	`, agentUserID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetExpiredMemories returns memories that have passed their valid_until
// or expires_at timestamp. Used by the retention poller.
func (s *Service) GetExpiredMemories(limit int) ([]*api.AgentMemory, error) {
	var results []*api.AgentMemory
	err := s.conn.Select(&results, `
		SELECT * FROM agent_memory
		WHERE (valid_until IS NOT NULL AND valid_until < NOW())
		   OR (expires_at IS NOT NULL AND expires_at < NOW())
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	return results, err
}

// DeleteMemoriesOlderThan removes non-pinned memories for an agent that
// were created before the given cutoff. Returns the number deleted.
func (s *Service) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) {
	query := `DELETE FROM agent_memory WHERE agent_id = $1 AND created_at < $2`
	if excludePinned {
		query += ` AND pinned = FALSE`
	}
	result, err := s.conn.Exec(query, agentID, olderThan)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteExpiredMemories removes memories past their valid_until or
// expires_at. Returns the number deleted.
func (s *Service) DeleteExpiredMemories(limit int) (int64, error) {
	result, err := s.conn.Exec(`
		DELETE FROM agent_memory
		WHERE id IN (
			SELECT id FROM agent_memory
			WHERE (valid_until IS NOT NULL AND valid_until < NOW())
			   OR (expires_at IS NOT NULL AND expires_at < NOW())
			LIMIT $1
		)
	`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetAgentsWithRetentionPolicy returns agents that have a non-null
// memory_retention_days value.
func (s *Service) GetAgentsWithRetentionPolicy() ([]struct {
	ID                  string `db:"id"`
	MemoryRetentionDays int    `db:"memory_retention_days"`
}, error) {
	var results []struct {
		ID                  string `db:"id"`
		MemoryRetentionDays int    `db:"memory_retention_days"`
	}
	err := s.conn.Select(&results, `
		SELECT id, memory_retention_days FROM agent
		WHERE memory_retention_days IS NOT NULL
	`)
	return results, err
}

// UpdateAgentRetentionDays sets the memory_retention_days for an agent.
// Pass nil to remove the retention policy.
func (s *Service) UpdateAgentRetentionDays(agentID string, days *int) error {
	_, err := s.conn.Exec(`
		UPDATE agent SET memory_retention_days = $1 WHERE id = $2
	`, days, agentID)
	return err
}

// --- Audit Log ---

// CreateAuditLogEntry inserts a new audit log record.
func (s *Service) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) {
	detail := entry.Detail
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}

	var id string
	err := s.conn.QueryRow(`
		INSERT INTO agent_audit_log (agent_id, agent_user_id, actor_type, actor_id, event_type, resource_type, resource_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, entry.AgentID, entry.AgentUserID, entry.ActorType, entry.ActorID,
		entry.EventType, entry.ResourceType, entry.ResourceID, detail).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// GetAuditLogForAgent returns audit log entries for an agent with pagination.
func (s *Service) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	var results []*api.AgentAuditLog
	err := s.conn.Select(&results, `
		SELECT * FROM agent_audit_log
		WHERE agent_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, agentID, limit, offset)
	return results, err
}

// GetAuditLogForUser returns audit log entries scoped to a specific user.
func (s *Service) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	var results []*api.AgentAuditLog
	err := s.conn.Select(&results, `
		SELECT * FROM agent_audit_log
		WHERE agent_user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, agentUserID, limit, offset)
	return results, err
}

// --- Identity Management ---

// UnlinkAgentIdentity removes an identity row. The agent_user remains
// (they may have other identities). Returns an error if the identity
// doesn't exist.
func (s *Service) UnlinkAgentIdentity(identityID string) error {
	result, err := s.conn.Exec(`DELETE FROM agent_identity WHERE id = $1`, identityID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// --- Data Export ---

// GetAllDataForUser assembles a full data export for a user: their
// user record, identities, memories, commitments, and audit log.
func (s *Service) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) {
	user, err := s.GetAgentUserByID(agentUserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, sql.ErrNoRows
	}

	identities, err := s.GetAgentIdentitiesByUserID(agentUserID)
	if err != nil {
		return nil, err
	}

	memories, err := s.GetAgentMemoriesForUser(agentUserID, false, 10000)
	if err != nil {
		return nil, err
	}

	var commitments []*api.AgentCommitment
	_ = s.conn.Select(&commitments, `
		SELECT * FROM agent_commitment
		WHERE agent_user_id = $1
		ORDER BY created_at DESC
	`, agentUserID)

	auditLog, _ := s.GetAuditLogForUser(agentUserID, 1000, 0)

	return &api.AgentDataExport{
		User:        user,
		Identities:  identities,
		Memories:    memories,
		Commitments: commitments,
		AuditLog:    auditLog,
		ExportedAt:  time.Now(),
	}, nil
}
