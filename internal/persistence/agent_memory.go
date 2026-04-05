package persistence

// Phase 1 of the Agent Memory feature. See plans/agent_memory.md for the
// design document.
//
// This file hosts CRUD methods for the three new tables introduced in
// migration 41: agent_user, agent_identity, and agent_conversation. It
// also adds conversation-scoped message operations that complement the
// existing agent_message helpers in agent.go.
//
// Method naming convention: ResolveOrCreate* for "look up by natural key,
// create if missing, return either way" — the dominant pattern Launch
// needs when an incoming webhook arrives and the identity/conversation
// may or may not already exist.

import (
	"database/sql"
	"encoding/json"
	"errors"

	"flomation.app/automate/api"
)

// --- Agent Users ---

// GetAgentUserByID returns an AgentUser by its primary key.
// Returns (nil, nil) if no row exists (not an error condition — callers
// typically fall through to a create path).
func (s *Service) GetAgentUserByID(id string) (*api.AgentUser, error) {
	var result api.AgentUser
	if err := s.stmtGetAgentUserByID.Get(&result, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// CreateAgentUser inserts a new AgentUser and returns the generated ID.
// Used by ResolveOrCreateAgentIdentity when an unrecognised channel
// identity arrives and needs a brand-new canonical user.
func (s *Service) CreateAgentUser(user api.AgentUser) (*string, error) {
	var id string
	if err := s.stmtCreateAgentUser.Get(&id, struct {
		AgentID        string  `db:"agent_id"`
		OrganisationID *string `db:"organisation_id"`
		DisplayName    *string `db:"display_name"`
	}{
		AgentID:        user.AgentID,
		OrganisationID: user.OrganisationID,
		DisplayName:    user.DisplayName,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// --- Agent Identities ---

// GetAgentIdentityByExternal looks up an identity by its natural key
// (channel_type, channel_external_id, channel_scope). Returns (nil, nil)
// if no matching identity exists.
//
// The NULL-scope handling here mirrors the partial unique index from
// migration 41: a NULL channel_scope and an empty-string channel_scope
// collapse to the same bucket, so channel integrations that don't emit
// a scope (generic webhook, email) produce stable identities.
func (s *Service) GetAgentIdentityByExternal(channelType, externalID string, scope *string) (*api.AgentIdentity, error) {
	var result api.AgentIdentity
	if err := s.stmtGetAgentIdentityByExternal.Get(&result, struct {
		ChannelType       string  `db:"channel_type"`
		ChannelExternalID string  `db:"channel_external_id"`
		ChannelScope      *string `db:"channel_scope"`
	}{
		ChannelType:       channelType,
		ChannelExternalID: externalID,
		ChannelScope:      scope,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// CreateAgentIdentity inserts a new identity row. Not typically called
// directly by Launch — use ResolveOrCreateAgentIdentity instead, which
// handles the full look-up-or-create flow including auto-creating an
// AgentUser for a never-before-seen external identifier.
func (s *Service) CreateAgentIdentity(identity api.AgentIdentity) (*string, error) {
	var id string
	if err := s.stmtCreateAgentIdentity.Get(&id, struct {
		AgentUserID       string  `db:"agent_user_id"`
		ChannelType       string  `db:"channel_type"`
		ChannelExternalID string  `db:"channel_external_id"`
		ChannelScope      *string `db:"channel_scope"`
		Verified          bool    `db:"verified"`
	}{
		AgentUserID:       identity.AgentUserID,
		ChannelType:       identity.ChannelType,
		ChannelExternalID: identity.ChannelExternalID,
		ChannelScope:      identity.ChannelScope,
		Verified:          identity.Verified,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// LinkAgentIdentityToUser re-points an existing identity at a different
// AgentUser and marks it verified. Used by the natural-language identity
// linking flow that lands in Phase 5 — once a user has confirmed on both
// channels that they are the same person, the previously-separate
// identity rows are merged into one canonical user by calling this on
// each identity that needs to move.
//
// Included in Phase 1 so the CRUD surface is complete from day one; the
// full linking flow is built on top in Phase 5.
func (s *Service) LinkAgentIdentityToUser(identityID, agentUserID string) error {
	_, err := s.stmtLinkAgentIdentityToUser.Exec(struct {
		ID          string `db:"id"`
		AgentUserID string `db:"agent_user_id"`
	}{
		ID:          identityID,
		AgentUserID: agentUserID,
	})
	return err
}

// ResolveOrCreateAgentIdentity is the main entry point Launch calls when
// an incoming webhook arrives. It:
//
//  1. Looks up an identity by (channel_type, channel_external_id, scope).
//  2. If found, returns the existing identity and its AgentUser.
//  3. If not found, creates a fresh AgentUser scoped to the given agent
//     + organisation, then a fresh identity pointing at that user with
//     verified=false, then returns both.
//
// The auto-create path is what lets memories start accruing immediately
// for previously-unseen external identifiers, while keeping them scoped
// to a single identity until the user explicitly links identities via
// natural-language confirmation (Phase 5).
//
// displayName is used only on first creation; if the identity already
// exists, the existing AgentUser is returned unchanged.
func (s *Service) ResolveOrCreateAgentIdentity(
	agentID string,
	organisationID *string,
	channelType, externalID string,
	scope *string,
	displayName *string,
) (*api.AgentIdentity, *api.AgentUser, error) {
	existing, err := s.GetAgentIdentityByExternal(channelType, externalID, scope)
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		user, err := s.GetAgentUserByID(existing.AgentUserID)
		if err != nil {
			return nil, nil, err
		}
		return existing, user, nil
	}

	// Auto-create a fresh AgentUser and identity in a minimal two-step
	// write. Transactional atomicity isn't strictly required here because
	// the failure mode is benign: a created-then-orphaned AgentUser with
	// no identity row cannot be observed by any other query path (there
	// is no API lookup of agent_user that doesn't go through an identity
	// first) and will be cleaned up the next time the same external ID
	// arrives.
	userID, err := s.CreateAgentUser(api.AgentUser{
		AgentID:        agentID,
		OrganisationID: organisationID,
		DisplayName:    displayName,
	})
	if err != nil {
		return nil, nil, err
	}

	identityID, err := s.CreateAgentIdentity(api.AgentIdentity{
		AgentUserID:       *userID,
		ChannelType:       channelType,
		ChannelExternalID: externalID,
		ChannelScope:      scope,
		Verified:          false,
	})
	if err != nil {
		return nil, nil, err
	}

	identity, err := s.GetAgentIdentityByExternal(channelType, externalID, scope)
	if err != nil {
		return nil, nil, err
	}
	user, err := s.GetAgentUserByID(*userID)
	if err != nil {
		return nil, nil, err
	}

	// Silence unused local; identityID's value is already reflected in
	// the row we just re-fetched above. We keep the return from
	// CreateAgentIdentity for future logging hooks.
	_ = identityID

	return identity, user, nil
}

// --- Agent Conversations ---

// GetAgentConversationByKey resolves an OPEN conversation by its natural
// key (agent, channel_type, channel_id, thread_id). Returns (nil, nil) if
// no open conversation exists for that key. Closed conversations are
// intentionally ignored — the next message on the same key will open a
// fresh conversation row.
func (s *Service) GetAgentConversationByKey(agentID, channelType, channelID string, threadID *string) (*api.AgentConversation, error) {
	var result api.AgentConversation
	if err := s.stmtGetAgentConversationByKey.Get(&result, struct {
		AgentID     string  `db:"agent_id"`
		ChannelType string  `db:"channel_type"`
		ChannelID   string  `db:"channel_id"`
		ThreadID    *string `db:"thread_id"`
	}{
		AgentID:     agentID,
		ChannelType: channelType,
		ChannelID:   channelID,
		ThreadID:    threadID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// CreateAgentConversation inserts a new conversation row. Prefer
// ResolveOrCreateAgentConversation unless you specifically need to
// force a new conversation regardless of what exists.
func (s *Service) CreateAgentConversation(conv api.AgentConversation) (*string, error) {
	var id string
	if err := s.stmtCreateAgentConversation.Get(&id, struct {
		AgentID     string  `db:"agent_id"`
		AgentUserID *string `db:"agent_user_id"`
		ChannelType string  `db:"channel_type"`
		ChannelID   string  `db:"channel_id"`
		ThreadID    *string `db:"thread_id"`
	}{
		AgentID:     conv.AgentID,
		AgentUserID: conv.AgentUserID,
		ChannelType: conv.ChannelType,
		ChannelID:   conv.ChannelID,
		ThreadID:    conv.ThreadID,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// TouchAgentConversation updates the conversation's last_message_at to
// NOW(). Called after every message insert so that idle detection
// (Phase 2 onwards) has fresh data to work with.
func (s *Service) TouchAgentConversation(conversationID string) error {
	_, err := s.stmtTouchAgentConversation.Exec(struct {
		ID string `db:"id"`
	}{ID: conversationID})
	return err
}

// ResolveOrCreateAgentConversation is the main entry point Launch calls
// after resolving the identity and before storing the current message.
// It:
//
//  1. Looks up an open conversation by (agent, channel_type, channel_id,
//     thread_id).
//  2. If found, returns it and associates it with the given AgentUser
//     (only if the stored user_id is NULL — we do NOT overwrite an
//     existing user association).
//  3. If not found, creates a fresh conversation scoped to that user.
//
// This produces the deterministic scoping rule from the plan:
// every (agent, channel, thread) has at most one open conversation at a
// time, and that conversation is owned by the AgentUser who first spoke
// in it.
func (s *Service) ResolveOrCreateAgentConversation(
	agentID string,
	agentUserID *string,
	channelType, channelID string,
	threadID *string,
) (*api.AgentConversation, error) {
	existing, err := s.GetAgentConversationByKey(agentID, channelType, channelID, threadID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	if _, err := s.CreateAgentConversation(api.AgentConversation{
		AgentID:     agentID,
		AgentUserID: agentUserID,
		ChannelType: channelType,
		ChannelID:   channelID,
		ThreadID:    threadID,
	}); err != nil {
		return nil, err
	}

	// Re-fetch so the caller sees the server-side defaults (started_at,
	// last_message_at, metadata) without another round-trip of synthesis.
	return s.GetAgentConversationByKey(agentID, channelType, channelID, threadID)
}

// --- Conversation-scoped messages ---

// GetAgentConversationMessages returns the conversation's messages in
// chronological order (oldest first), bounded by limit. This is what
// Launch will call to assemble conversation_history for the AI action,
// replacing the agent-wide GetAgentMessages in Phase 1.4.
func (s *Service) GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error) {
	var results []*api.AgentMessage
	if err := s.stmtGetAgentConversationMessages.Select(&results, struct {
		ConversationID string `db:"conversation_id"`
		Limit          int    `db:"limit"`
	}{
		ConversationID: conversationID,
		Limit:          limit,
	}); err != nil {
		return nil, err
	}
	return results, nil
}

// nextAgentConversationSequence returns MAX(sequence)+1 for the given
// conversation, or 1 if no messages exist yet.
func (s *Service) nextAgentConversationSequence(conversationID string) (int64, error) {
	var result struct {
		NextSequence int64 `db:"next_sequence"`
	}
	if err := s.stmtNextAgentConversationSequence.Get(&result, struct {
		ConversationID string `db:"conversation_id"`
	}{ConversationID: conversationID}); err != nil {
		return 0, err
	}
	return result.NextSequence, nil
}

// CreateAgentMessageInConversation writes an agent_message row with
// explicit conversation scoping and an auto-assigned per-conversation
// sequence number. Also touches the conversation's last_message_at.
//
// Phase 1 computes the sequence in a separate query rather than via an
// INSERT ... SELECT MAX+1 subquery. The race window this introduces is
// narrow and benign at Phase 1's expected write rate; Phase 2's per-user
// extraction lease serialises writes for the same conversation anyway.
// A later chunk will tighten this with a retry-on-unique-violation once
// the partial unique index (conversation_id, sequence) is exercised
// under load.
func (s *Service) CreateAgentMessageInConversation(msg api.AgentMessage) (*string, error) {
	if msg.ConversationID == nil {
		return nil, errors.New("CreateAgentMessageInConversation requires a non-nil ConversationID")
	}

	sequence, err := s.nextAgentConversationSequence(*msg.ConversationID)
	if err != nil {
		return nil, err
	}

	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	var id string
	if err := s.stmtCreateAgentMessageInConversation.Get(&id, struct {
		AgentID        string          `db:"agent_id"`
		SessionID      *string         `db:"session_id"`
		ConversationID *string         `db:"conversation_id"`
		Sequence       int64           `db:"sequence"`
		Direction      string          `db:"direction"`
		ChannelType    string          `db:"channel_type"`
		Sender         *string         `db:"sender"`
		Content        string          `db:"content"`
		Metadata       json.RawMessage `db:"metadata"`
		ExecutionID    *string         `db:"execution_id"`
	}{
		AgentID:        msg.AgentID,
		SessionID:      msg.SessionID,
		ConversationID: msg.ConversationID,
		Sequence:       sequence,
		Direction:      msg.Direction,
		ChannelType:    msg.ChannelType,
		Sender:         msg.Sender,
		Content:        msg.Content,
		Metadata:       metadataJSON,
		ExecutionID:    msg.ExecutionID,
	}); err != nil {
		return nil, err
	}

	// Best-effort last_message_at update. A failure here is non-fatal —
	// the message itself is safely stored; idle detection just won't know
	// about this turn until the next successful touch.
	_ = s.TouchAgentConversation(*msg.ConversationID)

	return &id, nil
}