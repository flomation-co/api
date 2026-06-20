package persistence

// Prior-conversation surface for agent reasoning loops.
//
// Two read paths land here:
//
//   1. GetRecentPriorConversations — called by inbound dispatch on
//      every message. Returns the last N session_summary memories
//      for the (agent, user) pair, each paired with the source
//      conversation's metadata. The agent sees these in the trigger
//      payload's `prior_conversations` field and can refer to any
//      by its conversation_id.
//
//   2. GetConversationMessagesForAgent — called by the executor's
//      agent/get_conversation tool when the LLM decides a summary
//      is relevant enough to drill into. Auth is enforced by the
//      WHERE clause: the conversation MUST belong to the requesting
//      agent AND the agent_user. No middleware to misconfigure.
//
// Both methods are pure SELECTs — no writes, no side effects, no
// expectation of strict consistency across rounds. The order is
// always created_at DESC for summaries and sequence ASC for
// messages, matching how the LLM expects to read them.

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// PriorConversationSummary is the wire shape returned to inbound
// dispatch and ultimately serialised into the agent's trigger data.
// Renamed-on-the-wire to make the LLM's role obvious: a summary is
// what it sees, a conversation_id is what it uses for lookup.
type PriorConversationSummary struct {
	ConversationID string     `db:"conversation_id" json:"conversation_id"`
	EndedAt        *time.Time `db:"ended_at" json:"ended_at,omitempty"`
	Summary        string     `db:"summary" json:"summary"`
	MessageCount   int64      `db:"message_count" json:"message_count"`
	ChannelType    string     `db:"channel_type" json:"channel_type"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
}

// PriorConversationMessage is the lookup-tool wire shape — narrower
// than the full AgentMessage type because agents only need the
// fields that affect their reasoning. Strips internal columns
// (session_id, metadata, embeddings) the LLM can't act on.
type PriorConversationMessage struct {
	Sequence  int64     `db:"sequence" json:"sequence"`
	Direction string    `db:"direction" json:"direction"`
	Sender    *string   `db:"sender" json:"sender,omitempty"`
	Content   string    `db:"content" json:"content"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// GetRecentPriorConversations returns up to `limit` session_summary
// memories for (agentID, agentUserID), most recent first, each
// joined to its source conversation for the conversation_id +
// channel_type + message_count + ended_at.
//
// A NULL agentUserID is rejected with no rows — anonymous/global
// summaries don't belong in a per-user "recent conversations"
// list. The caller should treat zero rows as "no prior context"
// rather than an error.
func (s *Service) GetRecentPriorConversations(agentID string, agentUserID string, limit int) ([]PriorConversationSummary, error) {
	if agentID == "" || agentUserID == "" {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}

	var rows []PriorConversationSummary
	q := `
		SELECT
			m.source_conversation AS conversation_id,
			c.ended_at            AS ended_at,
			m.body                AS summary,
			COALESCE((
				SELECT COUNT(1) FROM agent_message
				WHERE conversation_id = m.source_conversation
			), 0)                 AS message_count,
			c.channel_type        AS channel_type,
			m.created_at          AS created_at
		FROM agent_memory m
		INNER JOIN agent_conversation c ON c.id = m.source_conversation
		WHERE m.agent_id          = $1
		  AND m.agent_user_id     = $2
		  AND m.memory_type       = 'session_summary'
		  AND m.source_conversation IS NOT NULL
		ORDER BY m.created_at DESC
		LIMIT $3
	`
	if err := s.conn.Select(&rows, q, agentID, agentUserID, limit); err != nil {
		return nil, err
	}
	return rows, nil
}

// ErrConversationNotAccessible is returned by
// GetConversationMessagesForAgent when no row matches the supplied
// (conversation_id, agent_id, agent_user_id) triple. The sentinel
// lets HTTP handlers map this cleanly to a 404 — we deliberately
// don't distinguish "conversation doesn't exist" from "exists but
// you don't own it" to avoid leaking the existence of other users'
// conversations.
var ErrConversationNotAccessible = errors.New("conversation not accessible to this agent and user")

// GetConversationMessagesForAgent returns up to `maxMessages`
// messages from a conversation, ordered by sequence ASC, ONLY if the
// conversation belongs to the requesting agent AND the requesting
// agent_user. The auth check is the WHERE clause itself — no
// separate verification step that could be skipped.
//
// Returns wasTruncated=true when the conversation has more messages
// than the cap. The agent then knows the full text isn't on screen
// and can decide whether to ask for more or carry on with what it
// has.
func (s *Service) GetConversationMessagesForAgent(
	conversationID, agentID, agentUserID string,
	maxMessages int,
) (messages []PriorConversationMessage, ended *time.Time, totalCount int64, wasTruncated bool, err error) {
	if maxMessages <= 0 {
		maxMessages = 200
	}
	if conversationID == "" || agentID == "" || agentUserID == "" {
		err = ErrConversationNotAccessible
		return
	}

	// Auth + metadata fetch in one round-trip. If the conversation
	// doesn't match the auth triple, we return ErrConversationNotAccessible
	// — same response shape as "doesn't exist", deliberately.
	var meta struct {
		EndedAt *time.Time `db:"ended_at"`
	}
	authQ := `
		SELECT ended_at
		FROM agent_conversation
		WHERE id            = $1
		  AND agent_id      = $2
		  AND agent_user_id = $3
	`
	if qerr := s.conn.Get(&meta, authQ, conversationID, agentID, agentUserID); qerr != nil {
		if errors.Is(qerr, sql.ErrNoRows) {
			err = ErrConversationNotAccessible
			return
		}
		err = qerr
		return
	}
	ended = meta.EndedAt

	// Total count for the truncation flag. Cheap because of the
	// existing (conversation_id, sequence) index on agent_message.
	countQ := `SELECT COUNT(1) FROM agent_message WHERE conversation_id = $1`
	if qerr := s.conn.Get(&totalCount, countQ, conversationID); qerr != nil {
		err = qerr
		return
	}
	wasTruncated = totalCount > int64(maxMessages)

	// We always pull from the START of the conversation when
	// truncated rather than the end. The summary already covers the
	// gist; the model wanting more is usually looking for setup
	// detail or specific quotes near the top.
	msgQ := `
		SELECT sequence, direction, sender, content, created_at
		FROM agent_message
		WHERE conversation_id = $1
		ORDER BY sequence ASC
		LIMIT $2
	`
	if qerr := s.conn.Select(&messages, msgQ, conversationID, maxMessages); qerr != nil {
		err = qerr
		return
	}
	return
}

// Compile-time guard that PriorConversationSummary stays usable with
// sqlx.Select — keeps the public shape stable if someone edits the
// struct later.
var _ sqlx.Queryer = (*sqlx.DB)(nil)
