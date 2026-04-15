package poller

import (
	"context"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/embedding"
	"github.com/pgvector/pgvector-go"
	log "github.com/sirupsen/logrus"
)

// EmbeddingPersistence defines the DB methods the backfill poller needs.
type EmbeddingPersistence interface {
	GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error)
	UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error
}

// EmbeddingBackfillPoller generates embeddings for memories that were
// created without them. Runs every 15 seconds.
type EmbeddingBackfillPoller struct {
	persistence EmbeddingPersistence
	embedding   embedding.Provider
}

// StartEmbeddingBackfillPoller creates and starts the embedding backfill
// goroutine. No-ops if the embedding provider is nil.
func StartEmbeddingBackfillPoller(p EmbeddingPersistence, emb embedding.Provider) *EmbeddingBackfillPoller {
	if emb == nil {
		return nil
	}
	bp := &EmbeddingBackfillPoller{
		persistence: p,
		embedding:   emb,
	}
	go bp.watch()
	return bp
}

func (bp *EmbeddingBackfillPoller) watch() {
	time.Sleep(5 * time.Second)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Info("embedding backfill poller started (API-side, 15s interval)")

	for range ticker.C {
		bp.backfillBatch()
	}
}

func (bp *EmbeddingBackfillPoller) backfillBatch() {
	memories, err := bp.persistence.GetMemoriesWithoutEmbedding(10)
	if err != nil {
		log.WithError(err).Debug("embedding backfill: failed to fetch unembedded memories")
		return
	}
	if len(memories) == 0 {
		return
	}

	log.WithField("count", len(memories)).Debug("embedding backfill: processing batch")

	for _, mem := range memories {
		text := mem.Title
		if mem.Body != "" {
			if text != "" {
				text += ": "
			}
			text += mem.Body
		}
		if text == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		vec, err := bp.embedding.Embed(ctx, text)
		if err != nil {
			cancel()
			log.WithFields(log.Fields{
				"memory_id": mem.ID,
				"error":     err,
			}).Warn("embedding backfill: failed to generate embedding")
			continue
		}

		if err := bp.persistence.UpdateMemoryEmbedding(mem.ID, pgvector.NewVector(vec)); err != nil {
			cancel()
			log.WithFields(log.Fields{
				"memory_id": mem.ID,
				"error":     err,
			}).Warn("embedding backfill: failed to update embedding")
			continue
		}
		cancel()

		log.WithField("memory_id", mem.ID).Debug("embedding backfill: embedded")
	}
}
