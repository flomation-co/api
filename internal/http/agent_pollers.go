package http

import (
	"time"

	"flomation.app/automate/api/internal/agent"
	apiconfig "flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/mtls"
	"flomation.app/automate/api/internal/persistence"
	"flomation.app/automate/api/internal/poller"
	log "github.com/sirupsen/logrus"
)

// startPollers launches the API-side background pollers. These replace
// the Launch-side pollers that made HTTP round-trips back to the API.
// Phase 4: uses DirectFlowDispatcher instead of HTTP self-calls.
func (s *Service) startPollers(cfg *apiconfig.Config, p *persistence.Service) {
	dispatcher := agent.NewDirectFlowDispatcher(p, s.executionNotifier)

	// Wrap the direct dispatcher to satisfy the poller's FlowDispatcher interface.
	pollerDispatcher := &pollerDispatcherAdapter{d: dispatcher}

	// Retention poller (1h) — deletes expired and over-retention memories.
	poller.StartRetentionPoller(p)
	log.Info("API-side retention poller registered")

	// Commitment poller (30s) — fires due commitments.
	poller.StartCommitmentPoller(p, pollerDispatcher)
	log.Info("API-side commitment poller registered")

	// Pending action poller (15s) — dispatches confirmation prompts.
	poller.StartPendingActionPoller(p, pollerDispatcher)
	log.Info("API-side pending action poller registered")

	// Schedule poller (30s) — fires due agent schedules.
	poller.StartSchedulePoller(p, pollerDispatcher)
	log.Info("API-side schedule poller registered")

	// Embedding backfill poller (15s) — generates missing embeddings.
	if s.embeddingProvider != nil {
		poller.StartEmbeddingBackfillPoller(p, s.embeddingProvider)
		log.Info("API-side embedding backfill poller registered")
	}

	// Credit deduction sync poller (30s) — pushes deductions to billing service.
	if cfg.Billing.InternalURL != "" {
		billingClient, err := mtls.ClientOrDefault(cfg.TLS, 15*time.Second)
		if err != nil {
			log.WithError(err).Error("failed to create mTLS client for credit sync poller, using default")
		} else {
			poller.StartCreditSyncPoller(p, cfg.Billing.InternalURL, billingClient)
			log.Info("API-side credit sync poller registered")
		}
	}

	// Credential token refresh poller (60s) — proactively refreshes OAuth tokens.
	poller.StartCredentialRefreshPoller(p)

	// Google account refresh poller (60s) — proactively refreshes agent Google account tokens.
	if cfg.OAuth != nil {
		if google, ok := cfg.OAuth["google"]; ok {
			poller.StartGoogleAccountRefreshPoller(p, google.ClientID, google.ClientSecret)
		}
	}
}

// pollerDispatcherAdapter wraps *agent.DirectFlowDispatcher to satisfy
// the poller.FlowDispatcher interface. Both have the same DispatchFlow
// signature so this is just a type bridge.
type pollerDispatcherAdapter struct {
	d *agent.DirectFlowDispatcher
}

func (a *pollerDispatcherAdapter) DispatchFlow(flowID string, triggerID *string, data map[string]interface{}) error {
	return a.d.DispatchFlow(flowID, triggerID, data)
}
