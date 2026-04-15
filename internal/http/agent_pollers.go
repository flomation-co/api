package http

import (
	"fmt"

	apiconfig "flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	"flomation.app/automate/api/internal/poller"
	log "github.com/sirupsen/logrus"
)

// startPollers launches the API-side background pollers. These replace
// the Launch-side pollers that made HTTP round-trips back to the API.
//
// All pollers use direct persistence access and fail-open — a single
// poller failure never blocks other pollers or the HTTP service.
func (s *Service) startPollers(cfg *apiconfig.Config, p *persistence.Service) {
	// Self-referencing URL for flow dispatch. The pollers dispatch
	// orchestrator flows by calling the API's own internal endpoint.
	selfURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.HttpListenConfig.Port)

	dispatcher := poller.NewHTTPFlowDispatcher(selfURL)

	// Retention poller (1h) — deletes expired and over-retention memories.
	poller.StartRetentionPoller(p)
	log.Info("API-side retention poller registered")

	// Commitment poller (30s) — fires due commitments.
	poller.StartCommitmentPoller(p, dispatcher)
	log.Info("API-side commitment poller registered")

	// Pending action poller (15s) — dispatches confirmation prompts.
	poller.StartPendingActionPoller(p, dispatcher)
	log.Info("API-side pending action poller registered")

	// Embedding backfill poller (15s) — generates missing embeddings.
	if s.embeddingProvider != nil {
		poller.StartEmbeddingBackfillPoller(p, s.embeddingProvider)
		log.Info("API-side embedding backfill poller registered")
	}
}