package poller

import (
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/connector/emailoctopus"
	log "github.com/sirupsen/logrus"
)

// MarketingSyncPersistence is the narrow persistence surface the
// retry poller touches. Keeps unrelated persistence churn from
// rippling into this file.
type MarketingSyncPersistence interface {
	ListUsersNeedingMarketingSync(limit int) ([]*api.User, error)
	MarkUserMarketingSynced(userID string) error
	MarkUserMarketingSyncFailed(userID, reason string) error
}

// MarketingSyncPoller reconciles users.marketing_opt_in with their
// EmailOctopus list membership. The HTTP profile + welcome endpoints
// deliberately don't call EO inline — they reset
// marketing_synced_at to NULL and let this poller drain the queue
// in the background, so EO's availability never blocks a user's
// profile save.
//
// Picks up two kinds of rows:
//
//   - Never-synced (welcome_completed_at IS NOT NULL AND
//     marketing_synced_at IS NULL): user just made a marketing
//     choice, push it to EO.
//
//   - Failed previous sync (marketing_sync_error IS NOT NULL):
//     retry. Error is cleared on success or replaced with the new
//     reason on failure, so an operator can grep for stuck rows.
//
// Cadence is generous (60s); marketing sync is not latency-sensitive.
type MarketingSyncPoller struct {
	persistence MarketingSyncPersistence
	connector   *emailoctopus.Connector
	interval    time.Duration
	batchSize   int
}

const (
	marketingSyncInterval = 60 * time.Second
	marketingSyncBatch    = 50
)

// StartMarketingSyncPoller spins up the poller goroutine. Silently
// no-ops when the EmailOctopus connector is unconfigured — keeps
// local-dev environments quiet.
func StartMarketingSyncPoller(p MarketingSyncPersistence, eo *emailoctopus.Connector) *MarketingSyncPoller {
	if eo == nil || !eo.Configured() {
		log.Info("marketing sync poller: EmailOctopus not configured, skipping start")
		return nil
	}

	mp := &MarketingSyncPoller{
		persistence: p,
		connector:   eo,
		interval:    marketingSyncInterval,
		batchSize:   marketingSyncBatch,
	}
	go mp.watch()
	return mp
}

func (mp *MarketingSyncPoller) watch() {
	// Small initial settle to let other startup work finish before we
	// start hitting an external service.
	time.Sleep(15 * time.Second)

	ticker := time.NewTicker(mp.interval)
	defer ticker.Stop()

	log.WithField("interval", mp.interval.String()).Info("marketing sync poller started")

	for range ticker.C {
		mp.sweep()
	}
}

func (mp *MarketingSyncPoller) sweep() {
	users, err := mp.persistence.ListUsersNeedingMarketingSync(mp.batchSize)
	if err != nil {
		log.WithError(err).Warn("marketing sync poller: failed to list users")
		return
	}
	if len(users) == 0 {
		return
	}

	log.WithField("count", len(users)).Debug("marketing sync poller: processing")

	for _, u := range users {
		mp.syncOne(u)
	}
}

func (mp *MarketingSyncPoller) syncOne(u *api.User) {
	if u == nil || u.EmailAddress == nil || *u.EmailAddress == "" {
		// Without an email we can't do anything with EO. Stamp
		// failed so the row stops being polled until an operator
		// notices.
		if u != nil {
			_ = mp.persistence.MarkUserMarketingSyncFailed(u.ID, "missing email address")
		}
		return
	}

	l := log.WithFields(log.Fields{
		"user_id":          u.ID,
		"marketing_opt_in": u.MarketingOptIn,
	})

	var err error
	if u.MarketingOptIn {
		err = mp.connector.Subscribe(*u.EmailAddress, u.Name)
	} else {
		err = mp.connector.Unsubscribe(*u.EmailAddress)
	}

	if err != nil {
		l.WithError(err).Warn("marketing sync poller: EmailOctopus call failed")
		_ = mp.persistence.MarkUserMarketingSyncFailed(u.ID, err.Error())
		return
	}

	if err := mp.persistence.MarkUserMarketingSynced(u.ID); err != nil {
		l.WithError(err).Warn("marketing sync poller: failed to mark synced; will retry")
		return
	}
	l.Debug("marketing sync poller: synced")
}
