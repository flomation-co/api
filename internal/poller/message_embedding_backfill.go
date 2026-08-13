package poller

import (
	"context"
	"strings"
	"time"

	"flomation.app/automate/api/internal/embedding"
	"flomation.app/automate/api/internal/persistence"
	"github.com/pgvector/pgvector-go"
	log "github.com/sirupsen/logrus"
)

// MessageEmbeddingPersistence is the DB surface the message backfill poller needs.
type MessageEmbeddingPersistence interface {
	GetAgentMessagesWithoutEmbedding(limit int) ([]persistence.AgentMessageToEmbed, error)
	UpdateAgentMessageEmbedding(id string, embedding pgvector.Vector) error
}

// MessageEmbeddingBackfillPoller generates embeddings for agent_message rows
// stored without one (Phase 2 semantic conversation search). Doing it offline
// keeps message handling off the embeddings-provider hot path — a message is
// full-text searchable immediately and becomes semantically searchable within a
// tick. Runs every 15s; no-ops if the provider is nil.
type MessageEmbeddingBackfillPoller struct {
	persistence MessageEmbeddingPersistence
	embedding   embedding.Provider
}

// StartMessageEmbeddingBackfillPoller starts the goroutine (nil provider = no-op).
func StartMessageEmbeddingBackfillPoller(p MessageEmbeddingPersistence, emb embedding.Provider) *MessageEmbeddingBackfillPoller {
	if emb == nil {
		return nil
	}
	bp := &MessageEmbeddingBackfillPoller{persistence: p, embedding: emb}
	go bp.watch()
	return bp
}

func (bp *MessageEmbeddingBackfillPoller) watch() {
	time.Sleep(7 * time.Second) // stagger against the memory backfill poller

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Info("message embedding backfill poller started (API-side, 15s interval)")

	for range ticker.C {
		bp.backfillBatch()
	}
}

func (bp *MessageEmbeddingBackfillPoller) backfillBatch() {
	msgs, err := bp.persistence.GetAgentMessagesWithoutEmbedding(10)
	if err != nil {
		log.WithError(err).Debug("message embedding backfill: failed to fetch unembedded messages")
		return
	}
	if len(msgs) == 0 {
		return
	}

	for _, m := range msgs {
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		vec, err := bp.embedding.Embed(ctx, text)
		if err != nil {
			cancel()
			log.WithField("message_id", m.ID).WithError(err).Warn("message embedding backfill: failed to generate embedding")
			continue
		}
		if err := bp.persistence.UpdateAgentMessageEmbedding(m.ID, pgvector.NewVector(vec)); err != nil {
			cancel()
			log.WithField("message_id", m.ID).WithError(err).Warn("message embedding backfill: failed to update embedding")
			continue
		}
		cancel()
	}
}
