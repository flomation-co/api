package poller

import (
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/agent"
	log "github.com/sirupsen/logrus"
)

// ConversationSweeperPersistence is exactly the slice of the persistence
// API the sweeper touches — kept narrow to make testing trivial and to
// stop unrelated persistence churn from rippling into this file.
//
// The agent.SummaryPersistence embed gives GenerateSessionSummary
// everything it needs (DispatchExtraction surface +
// GetAgentConversationMessages), and the two extra methods are the
// sweeper's own work: find stale conversations, atomically claim each.
type ConversationSweeperPersistence interface {
	agent.SummaryPersistence
	ListStaleOpenConversations(limit int) ([]*api.AgentConversation, error)
	CloseAgentConversationIfOpen(conversationID string) (bool, error)
}

// ConversationSweeper closes conversations whose idle timeout has
// elapsed without the user ever returning, and fires a session summary
// for each. This is the abandoned-conversation tail that the in-band
// close-on-next-message path (inbound.go) cannot reach — if no next
// message ever arrives, the conversation stays open forever and never
// gets summarised.
//
// Runs every conversationSweepInterval. The interval is intentionally
// generous: agents care about "summary lands eventually", not "lands
// promptly", and a chatty install with hundreds of stale conversations
// would otherwise hammer the extraction flow.
type ConversationSweeper struct {
	persistence ConversationSweeperPersistence
	notifier    agent.ExecutionNotifier
	interval    time.Duration
	batchSize   int
}

const (
	conversationSweepInterval = 5 * time.Minute
	conversationSweepBatch    = 50
)

// StartConversationSweeperPoller spins up the sweeper goroutine.
func StartConversationSweeperPoller(p ConversationSweeperPersistence, n agent.ExecutionNotifier) *ConversationSweeper {
	cs := &ConversationSweeper{
		persistence: p,
		notifier:    n,
		interval:    conversationSweepInterval,
		batchSize:   conversationSweepBatch,
	}
	go cs.watch()
	return cs
}

func (cs *ConversationSweeper) watch() {
	// Initial settle: avoid racing the BootstrapExtractionFlow on startup
	// (the sweeper depends on the canonical extraction flow being in
	// place before it can dispatch summaries).
	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(cs.interval)
	defer ticker.Stop()

	log.WithField("interval", cs.interval.String()).Info("conversation sweeper started")

	for range ticker.C {
		cs.sweep()
	}
}

func (cs *ConversationSweeper) sweep() {
	convs, err := cs.persistence.ListStaleOpenConversations(cs.batchSize)
	if err != nil {
		log.WithError(err).Warn("conversation sweeper: failed to list stale conversations")
		return
	}
	if len(convs) == 0 {
		return
	}

	log.WithField("count", len(convs)).Debug("conversation sweeper: processing stale conversations")

	for _, conv := range convs {
		cs.closeAndSummarise(conv)
	}
}

func (cs *ConversationSweeper) closeAndSummarise(conv *api.AgentConversation) {
	l := log.WithFields(log.Fields{
		"conversation_id": conv.ID,
		"agent_id":        conv.AgentID,
	})

	// Atomic claim: only the goroutine that flips ended_at from NULL to
	// NOW() proceeds to fire the summary. Lets multiple API instances
	// run sweepers concurrently without producing duplicate summaries.
	claimed, err := cs.persistence.CloseAgentConversationIfOpen(conv.ID)
	if err != nil {
		l.WithError(err).Warn("conversation sweeper: failed to close stale conversation")
		return
	}
	if !claimed {
		// Another writer (inbound message, parallel sweeper) closed it
		// first — they own the summary.
		return
	}

	agent.GenerateSessionSummary(cs.persistence, cs.notifier, conv.AgentID, conv.ID, conv.AgentUserID)
	l.Debug("conversation sweeper: closed and summarised stale conversation")
}
