package persistence

// Phase 2 of the Agent Memory feature — CRUD for the three new tables
// added in migration 42:
//
//   - agent_memory         (durable facts / preferences / feedback)
//   - agent_pending_action (natural-language intents awaiting confirmation)
//   - agent_commitment     (promises awaiting a schedule or signal)
//
// See plans/agent_memory.md for the full design. This file deliberately
// sits alongside agent_memory.go (Phase 1: identity + conversation CRUD)
// so the phase boundary stays legible in diffs and grep.
//
// Method naming mirrors Phase 1: Resolve* for natural-key lookups, Create*
// for inserts, Get* for primary-key fetches, Update*Status for status
// transitions, and plural verbs (GetAgentMemoriesForUser) for listings.
// No magic — every method is a thin wrapper over one prepared statement.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"flomation.app/automate/api"
	pgvector "github.com/pgvector/pgvector-go"
)

// --- Agent Memories ---

// CreateAgentMemory inserts a new memory row and returns the generated
// ID. Callers (extraction pipeline, agent/remember action) are expected
// to have already applied their own confidence threshold before calling
// — the persistence layer is unconditional.
func (s *Service) CreateAgentMemory(mem api.AgentMemory) (*string, error) {
	// payload fields don't apply here but confidence needs a sensible
	// default if the caller forgot to set one. Zero confidence is almost
	// never intended; 1.0 is the natural default for manual writes.
	if mem.Confidence == 0 {
		mem.Confidence = 1.0
	}
	var id string
	if err := s.stmtCreateAgentMemory.Get(&id, struct {
		AgentID            string           `db:"agent_id"`
		AgentUserID        *string          `db:"agent_user_id"`
		Scope              string           `db:"scope"`
		MemoryType         string           `db:"memory_type"`
		Title              string           `db:"title"`
		Body               string           `db:"body"`
		SourceConversation *string          `db:"source_conversation"`
		SourceMessage      *string          `db:"source_message"`
		Confidence         float64          `db:"confidence"`
		Pinned             bool             `db:"pinned"`
		ExpiresAt          *time.Time       `db:"expires_at"`
		Embedding          *pgvector.Vector `db:"embedding"`
	}{
		AgentID:            mem.AgentID,
		AgentUserID:        mem.AgentUserID,
		Scope:              mem.Scope,
		MemoryType:         mem.MemoryType,
		Title:              mem.Title,
		Body:               mem.Body,
		SourceConversation: mem.SourceConversation,
		SourceMessage:      mem.SourceMessage,
		Confidence:         mem.Confidence,
		Pinned:             mem.Pinned,
		ExpiresAt:          mem.ExpiresAt,
		Embedding:          mem.Embedding,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// GetAgentMemoryByID returns a memory by primary key, or (nil, nil) if
// no row exists.
func (s *Service) GetAgentMemoryByID(id string) (*api.AgentMemory, error) {
	var result api.AgentMemory
	if err := s.stmtGetAgentMemoryByID.Get(&result, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// GetAgentMemoriesForUser returns memories for a given agent_user,
// optionally restricted to pinned-only rows. This is the primary read
// path used by Launch's system prompt assembler in Phase 2b.
//
// Filter semantics:
//   - pinnedOnly=true → returns only pinned rows (preference + feedback
//     types in practice, since those are the auto-pinned types).
//   - limit<=0        → server-side default of 100.
//
// Results are ordered by pinned DESC (pins first), then created_at DESC
// so the assembler gets a stable, recency-biased view. Type-filtered
// retrieval is deliberately not exposed in Phase 2a; Phase 4's pgvector
// top-K query will replace this entirely.
func (s *Service) GetAgentMemoriesForUser(
	agentUserID string,
	pinnedOnly bool,
	limit int,
) ([]*api.AgentMemory, error) {
	if limit <= 0 {
		limit = 100
	}
	var results []*api.AgentMemory
	if err := s.stmtGetAgentMemoriesForUser.Select(&results, struct {
		AgentUserID string `db:"agent_user_id"`
		PinnedOnly  bool   `db:"pinned_only"`
		Limit       int    `db:"limit"`
	}{
		AgentUserID: agentUserID,
		PinnedOnly:  pinnedOnly,
		Limit:       limit,
	}); err != nil {
		return nil, err
	}
	return results, nil
}

// DeleteAgentMemory hard-deletes a memory row. Called by the agent/forget
// executor action once a forget confirmation has been resolved. The
// retention job in Phase 6 also uses this to clear expired rows.
func (s *Service) DeleteAgentMemory(id string) error {
	_, err := s.stmtDeleteAgentMemory.Exec(struct {
		ID string `db:"id"`
	}{ID: id})
	return err
}

// TouchAgentMemoryLastUsed stamps last_used_at = NOW() on a memory row.
// The system prompt assembler calls this on the memories it surfaces so
// retention can prefer keeping frequently-referenced memories alive.
// Errors are intentionally non-fatal at the call site — missing a touch
// only affects retention ordering, never correctness of the reply.
func (s *Service) TouchAgentMemoryLastUsed(id string) error {
	_, err := s.stmtTouchAgentMemoryLastUsed.Exec(struct {
		ID string `db:"id"`
	}{ID: id})
	return err
}

// --- Pending Actions ---

// CreateAgentPendingAction inserts a new pending action. The status
// defaults to 'awaiting_confirmation' via the column default; callers
// only need to set a non-default status when bootstrapping from an
// already-resolved state (tests, migrations from other systems).
func (s *Service) CreateAgentPendingAction(pa api.AgentPendingAction) (*string, error) {
	payload := pa.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	status := pa.Status
	if status == "" {
		status = "awaiting_confirmation"
	}
	var id string
	if err := s.stmtCreateAgentPendingAction.Get(&id, struct {
		AgentID            string          `db:"agent_id"`
		AgentUserID        string          `db:"agent_user_id"`
		Type               string          `db:"type"`
		Payload            json.RawMessage `db:"payload"`
		Evidence           string          `db:"evidence"`
		Status             string          `db:"status"`
		SourceConversation *string         `db:"source_conversation"`
		SourceMessage      *string         `db:"source_message"`
		ExpiresAt          *time.Time      `db:"expires_at"`
	}{
		AgentID:            pa.AgentID,
		AgentUserID:        pa.AgentUserID,
		Type:               pa.Type,
		Payload:            payload,
		Evidence:           pa.Evidence,
		Status:             status,
		SourceConversation: pa.SourceConversation,
		SourceMessage:      pa.SourceMessage,
		ExpiresAt:          pa.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// GetAgentPendingActionByID returns a single pending action or (nil, nil).
func (s *Service) GetAgentPendingActionByID(id string) (*api.AgentPendingAction, error) {
	var result api.AgentPendingAction
	if err := s.stmtGetAgentPendingActionByID.Get(&result, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// GetOpenPendingActionsForUser returns all pending actions for a user
// that are still awaiting some form of resolution (awaiting_confirmation
// or confirmed_here_awaiting_other_side). Launch calls this on every
// turn to decide whether to inject a confirmation prompt into the
// assembled system prompt.
func (s *Service) GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error) {
	var results []*api.AgentPendingAction
	if err := s.stmtGetOpenPendingActionsForUser.Select(&results, struct {
		AgentUserID string `db:"agent_user_id"`
	}{AgentUserID: agentUserID}); err != nil {
		return nil, err
	}
	return results, nil
}

// UpdatePendingActionStatus transitions a pending action to a new status
// and, for terminal states (executed, declined, expired), stamps
// resolved_at = NOW(). Non-terminal transitions leave resolved_at alone.
func (s *Service) UpdatePendingActionStatus(id, status string) error {
	_, err := s.stmtUpdatePendingActionStatus.Exec(struct {
		ID     string `db:"id"`
		Status string `db:"status"`
	}{ID: id, Status: status})
	return err
}

// GetUnnotifiedPendingActions returns pending actions that haven't been
// notified yet (notified_at IS NULL) and are awaiting confirmation.
// Used by the Launch pending action poller.
func (s *Service) GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error) {
	var results []*api.AgentPendingAction
	err := s.conn.Select(&results, `
		SELECT * FROM agent_pending_action
		WHERE status = 'awaiting_confirmation'
		  AND notified_at IS NULL
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	return results, err
}

// MarkPendingActionNotified stamps notified_at = NOW() on a pending action.
func (s *Service) MarkPendingActionNotified(id string) error {
	_, err := s.conn.Exec(`
		UPDATE agent_pending_action SET notified_at = NOW() WHERE id = $1
	`, id)
	return err
}

// --- Commitments ---

// CreateAgentCommitment inserts a new commitment. Payload defaults to an
// empty object; status defaults to 'pending' via the column default.
func (s *Service) CreateAgentCommitment(c api.AgentCommitment) (*string, error) {
	payload := c.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	// Condition is nullable; normalise nil/empty to a proper SQL NULL by
	// passing a nil pointer when the caller didn't supply one.
	var condition interface{}
	if c.Condition != nil && len(*c.Condition) > 0 {
		condition = []byte(*c.Condition)
	}
	status := c.Status
	if status == "" {
		status = "pending"
	}
	madeBy := c.MadeBy
	if madeBy == "" {
		madeBy = "assistant"
	}
	var id string
	if err := s.stmtCreateAgentCommitment.Get(&id, map[string]interface{}{
		"agent_id":            c.AgentID,
		"agent_user_id":       c.AgentUserID,
		"conversation_id":     c.ConversationID,
		"kind":                c.Kind,
		"description":         c.Description,
		"payload":             []byte(payload),
		"trigger_type":        c.TriggerType,
		"due_at":              c.DueAt,
		"condition":           condition,
		"status":              status,
		"source_conversation": c.SourceConversation,
		"source_message":      c.SourceMessage,
		"made_by":             madeBy,
		"expires_at":          c.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// GetAgentCommitmentByID returns one commitment or (nil, nil).
func (s *Service) GetAgentCommitmentByID(id string) (*api.AgentCommitment, error) {
	var result api.AgentCommitment
	if err := s.stmtGetAgentCommitmentByID.Get(&result, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// GetDueCommitments returns pending commitments whose due_at is in the
// past, ordered oldest-first, bounded by limit. This is the hot path the
// Phase 3 commitment poller will call on its 30-second cadence — the
// query is backed by the idx_agent_commitment_due_pending partial index
// so it stays cheap even with millions of fulfilled rows in the table.
//
// Phase 2a ships this method so Phase 3 only needs to add the polling
// loop and the synthetic trigger dispatch, not new persistence code.
func (s *Service) GetDueCommitments(limit int) ([]*api.AgentCommitment, error) {
	if limit <= 0 {
		limit = 100
	}
	var results []*api.AgentCommitment
	if err := s.stmtGetDueCommitments.Select(&results, struct {
		Limit int `db:"limit"`
	}{Limit: limit}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetCommitmentsForUser returns every commitment belonging to a user,
// newest first, for the Phase 6 profile page.
func (s *Service) GetCommitmentsForUser(agentUserID string, limit int) ([]*api.AgentCommitment, error) {
	if limit <= 0 {
		limit = 100
	}
	var results []*api.AgentCommitment
	if err := s.stmtGetCommitmentsForUser.Select(&results, struct {
		AgentUserID string `db:"agent_user_id"`
		Limit       int    `db:"limit"`
	}{AgentUserID: agentUserID, Limit: limit}); err != nil {
		return nil, err
	}
	return results, nil
}

// UpdateCommitmentStatus transitions a commitment through its lifecycle
// (pending → firing → fulfilled, or pending → cancelled, or → expired)
// and stamps the corresponding timestamp column so the profile page can
// render the transition history without a separate audit table.
func (s *Service) UpdateCommitmentStatus(id, status string) error {
	_, err := s.stmtUpdateCommitmentStatus.Exec(struct {
		ID     string `db:"id"`
		Status string `db:"status"`
	}{ID: id, Status: status})
	return err
}
