package persistence

// Phase 5 of the Agent Memory feature — natural-language identity linking.
//
// Provides the persistence layer for:
//   - Looking up an identity by channel + external ID (safety check)
//   - Merging two agent_user records: re-pointing identities, transferring
//     memories and commitments from the source user to the target user
//   - Getting a pending action by agent_user_id and type (for confirmation matching)

import (
	"database/sql"
	"errors"
	"fmt"

	"flomation.app/automate/api"
)

// GetAgentIdentitiesByUserID returns all identities for a given agent_user.
func (s *Service) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) {
	var results []*api.AgentIdentity
	if err := s.stmtGetAgentIdentitiesByUserID.Select(&results, struct {
		AgentUserID string `db:"agent_user_id"`
	}{AgentUserID: agentUserID}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return results, nil
}

// LookupIdentity checks whether a specific channel identity exists for a
// given agent. Returns the identity and its owning agent_user, or (nil, nil)
// if no such identity exists. Used as a safety check before allowing identity
// link proposals — the target must already be known to the agent.
func (s *Service) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) {
	identity, err := s.GetAgentIdentityByExternal(channelType, externalID, nil)
	if err != nil {
		return nil, nil, err
	}
	if identity == nil {
		return nil, nil, nil
	}

	user, err := s.GetAgentUserByID(identity.AgentUserID)
	if err != nil {
		return nil, nil, err
	}
	return identity, user, nil
}

// MergeAgentUsers transfers all identities, memories, and commitments from
// sourceUserID to targetUserID, then deletes the source user. This is the
// transactional merge that executes when both sides confirm an identity link.
//
// Steps:
//  1. Re-point all agent_identity rows from source → target
//  2. Transfer all agent_memory rows from source → target
//  3. Transfer all agent_commitment rows from source → target
//  4. Transfer all agent_pending_action rows from source → target
//  5. Delete the source agent_user row
func (s *Service) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error {
	tx, err := s.conn.Beginx()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Re-point identities
	if _, err := tx.Exec(`UPDATE agent_identity SET agent_user_id = $1 WHERE agent_user_id = $2`, targetUserID, sourceUserID); err != nil {
		return fmt.Errorf("transfer identities: %w", err)
	}

	// 2. Transfer memories (skip exact title duplicates)
	if _, err := tx.Exec(`
		UPDATE agent_memory SET agent_user_id = $1
		WHERE agent_user_id = $2
		  AND title NOT IN (SELECT title FROM agent_memory WHERE agent_user_id = $1)
	`, targetUserID, sourceUserID); err != nil {
		return fmt.Errorf("transfer memories: %w", err)
	}
	// Delete remaining source memories (duplicates)
	if _, err := tx.Exec(`DELETE FROM agent_memory WHERE agent_user_id = $1`, sourceUserID); err != nil {
		return fmt.Errorf("delete duplicate memories: %w", err)
	}

	// 3. Transfer commitments
	if _, err := tx.Exec(`UPDATE agent_commitment SET agent_user_id = $1 WHERE agent_user_id = $2`, targetUserID, sourceUserID); err != nil {
		return fmt.Errorf("transfer commitments: %w", err)
	}

	// 4. Transfer pending actions
	if _, err := tx.Exec(`UPDATE agent_pending_action SET agent_user_id = $1 WHERE agent_user_id = $2`, targetUserID, sourceUserID); err != nil {
		return fmt.Errorf("transfer pending actions: %w", err)
	}

	// 5. Transfer conversations
	if _, err := tx.Exec(`UPDATE agent_conversation SET agent_user_id = $1 WHERE agent_user_id = $2`, targetUserID, sourceUserID); err != nil {
		return fmt.Errorf("transfer conversations: %w", err)
	}

	// 6. Delete the source user
	if _, err := tx.Exec(`DELETE FROM agent_user WHERE id = $1`, sourceUserID); err != nil {
		return fmt.Errorf("delete source user: %w", err)
	}

	return tx.Commit()
}

// GetPendingActionByUserAndType finds an open pending action of a specific
// type for a given user. Used by the confirmation processor to match a
// confirmation to its originating pending action when the extraction pipeline
// doesn't provide a pending_action_id.
func (s *Service) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) {
	var result api.AgentPendingAction
	if err := s.stmtGetPendingActionByUserAndType.Get(&result, struct {
		AgentUserID string `db:"agent_user_id"`
		Type        string `db:"type"`
	}{
		AgentUserID: agentUserID,
		Type:        actionType,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}
