package http

import (
	"time"

	"flomation.app/automate/api/internal/agent"
	apiconfig "flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/connector/emailoctopus"
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

	// Resume poller (15s) — wakes suspended executions whose resume_at has
	// passed: Human-in-the-Loop timeouts (routing to the timeout handle) and
	// plain Wait-action auto-resumes.
	poller.StartResumePoller(p, s.executionNotifier)
	log.Info("API-side resume poller registered")

	// Embedding backfill poller (15s) — generates missing embeddings.
	if s.embeddingProvider != nil {
		poller.StartEmbeddingBackfillPoller(p, s.embeddingProvider)
		log.Info("API-side embedding backfill poller registered")
	}

	// Conversation sweeper (5m) — closes abandoned conversations whose
	// idle timeout has elapsed and fires session summaries for them.
	// Without this, conversations the user never returned to would
	// remain open indefinitely and produce zero summaries.
	poller.StartConversationSweeperPoller(p, s.executionNotifier)
	log.Info("API-side conversation sweeper registered")

	// Blob GC poller (1h) — sweeps expired blob_object rows
	// (tool_output @ 1h TTL, inbound @ 30d TTL) and prunes the
	// blob_quota_daily counter table.
	poller.StartBlobGCPoller(p)
	log.Info("API-side blob GC poller registered")

	// Plan tick poller (30s) — fallback sweep for active plans whose
	// next_check_at is past. Reactive wake-ups from plan/create, task
	// completion writeback, and TickPlan's own not_before-aware
	// scheduling handle the responsive cases; this catches
	// time-gated tasks + any reactive wake-up that didn't fire.
	poller.StartPlanTickPoller(p)
	log.Info("API-side plan tick poller registered")

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

	// Marketing sync poller (60s) — reconciles users.marketing_opt_in
	// with EmailOctopus list membership. The profile + welcome
	// endpoints push state changes into a NULL-synced row; this
	// poller drains the queue and updates EO. Silently no-ops when
	// EmailOctopus isn't configured (local dev).
	poller.StartMarketingSyncPoller(p, emailoctopus.NewConnector(cfg))
	log.Info("API-side marketing sync poller registered")

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
