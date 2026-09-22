package persistence

import (
	"sort"

	pgvector "github.com/pgvector/pgvector-go"
)

// SearchAgentMessagesByEmbedding runs a cosine-similarity (HNSW) search over the
// same corpus as SearchAgentMessages — every message between this agent and user,
// across all channels — scoped strictly by (agent_id, agent_user_id). Rank is the
// cosine similarity (1 − distance). Rows without an embedding (not yet backfilled)
// are skipped. Empty scope returns no rows.
func (s *Service) SearchAgentMessagesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, limit int) ([]AgentMessageSearchResult, error) {
	if agentID == "" || agentUserID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > agentMessageSearchMaxLimit {
		limit = agentMessageSearchDefaultLimit
	}

	const q = `
		SELECT m.id,
		       m.conversation_id,
		       m.channel_type,
		       m.direction,
		       m.sender,
		       m.content,
		       m.created_at,
		       1 - (m.embedding <=> $3) AS rank
		FROM agent_message m
		JOIN agent_conversation c ON c.id = m.conversation_id
		WHERE c.agent_id = $1
		  AND c.agent_user_id = $2
		  AND m.embedding IS NOT NULL
		ORDER BY m.embedding <=> $3
		LIMIT $4`

	var out []AgentMessageSearchResult
	if err := s.conn.Select(&out, q, agentID, agentUserID, embedding, limit); err != nil {
		return nil, err
	}
	return out, nil
}

// AgentMessageToEmbed is a message awaiting an embedding (backfill).
type AgentMessageToEmbed struct {
	ID      string `db:"id"`
	Content string `db:"content"`
}

// GetAgentMessagesWithoutEmbedding returns up to `limit` non-empty messages that
// have no embedding yet, newest first (recent history is the likeliest to be
// searched). Drives the message-embedding backfill poller.
func (s *Service) GetAgentMessagesWithoutEmbedding(limit int) ([]AgentMessageToEmbed, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	const q = `
		SELECT id, content
		FROM agent_message
		WHERE embedding IS NULL
		  AND content IS NOT NULL
		  AND length(btrim(content)) > 0
		ORDER BY created_at DESC
		LIMIT $1`
	var out []AgentMessageToEmbed
	if err := s.conn.Select(&out, q, limit); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAgentMessageEmbedding sets the embedding on a message row.
func (s *Service) UpdateAgentMessageEmbedding(id string, embedding pgvector.Vector) error {
	_, err := s.conn.Exec(`UPDATE agent_message SET embedding = $2 WHERE id = $1`, id, embedding)
	return err
}

// rrfK is the Reciprocal Rank Fusion constant. 60 is the widely-used default —
// large enough that a top-1 hit in one list doesn't utterly dominate a strong
// consensus across both.
const rrfK = 60

// FuseByRRF merges several ranked result lists into one using Reciprocal Rank
// Fusion: each list contributes 1/(k + rank) per item, summed by message id, so
// a message ranked highly by BOTH full-text and semantic search rises to the top
// even if neither ranked it first. Deduped by id; result Rank is the RRF score;
// ties broken by recency; truncated to limit.
func FuseByRRF(lists [][]AgentMessageSearchResult, limit int) []AgentMessageSearchResult {
	scores := make(map[string]float64)
	byID := make(map[string]AgentMessageSearchResult)
	for _, list := range lists {
		for rank, r := range list {
			scores[r.ID] += 1.0 / float64(rrfK+rank+1)
			if _, seen := byID[r.ID]; !seen {
				byID[r.ID] = r
			}
		}
	}
	out := make([]AgentMessageSearchResult, 0, len(byID))
	for id, r := range byID {
		r.Rank = scores[id]
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
