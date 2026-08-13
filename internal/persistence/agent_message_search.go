package persistence

import (
	"strings"
	"time"
)

// AgentMessageSearchResult is one hit from an agent's conversation history with
// a user (full-text or semantic). ID identifies the message so hybrid fusion can
// dedupe across the two rankings.
type AgentMessageSearchResult struct {
	ID             string    `db:"id" json:"-"`
	ConversationID string    `db:"conversation_id" json:"conversation_id"`
	ChannelType    string    `db:"channel_type" json:"channel_type"`
	Direction      string    `db:"direction" json:"direction"`
	Sender         *string   `db:"sender" json:"sender,omitempty"`
	Content        string    `db:"content" json:"content"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	Rank           float64   `db:"rank" json:"rank"`
}

// agentMessageSearchDefaultLimit / Max bound the result set.
const (
	agentMessageSearchDefaultLimit = 20
	agentMessageSearchMaxLimit     = 100
)

// SearchAgentMessages runs a full-text search over EVERY message exchanged
// between the given agent and user — across ALL of their conversations and
// channels — scoped strictly by (agent_id, agent_user_id). Results are ranked by
// relevance then recency. An empty query or missing scope returns no rows (the
// caller treats "inaccessible" and "no matches" identically, so nothing leaks
// about another user's or agent's history).
func (s *Service) SearchAgentMessages(agentID, agentUserID, query string, limit int) ([]AgentMessageSearchResult, error) {
	if agentID == "" || agentUserID == "" || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > agentMessageSearchMaxLimit {
		limit = agentMessageSearchDefaultLimit
	}

	// websearch_to_tsquery is user-input tolerant (never errors on stray
	// operators), so the AI-supplied query is safe to pass straight through.
	const q = `
		SELECT m.id,
		       m.conversation_id,
		       m.channel_type,
		       m.direction,
		       m.sender,
		       m.content,
		       m.created_at,
		       ts_rank(m.content_tsv, websearch_to_tsquery('english', $3)) AS rank
		FROM agent_message m
		JOIN agent_conversation c ON c.id = m.conversation_id
		WHERE c.agent_id = $1
		  AND c.agent_user_id = $2
		  AND m.content_tsv @@ websearch_to_tsquery('english', $3)
		ORDER BY rank DESC, m.created_at DESC
		LIMIT $4`

	var out []AgentMessageSearchResult
	if err := s.conn.Select(&out, q, agentID, agentUserID, query, limit); err != nil {
		return nil, err
	}
	return out, nil
}
