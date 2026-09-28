package poller

// Delivery of the onboarding email sequence.
//
// The poller's real job is not sending — it is deciding not to. Three emails
// arriving on a schedule regardless of what the reader has done is the thing
// that makes onboarding sequences worth filtering, so every email is checked
// against its own goal immediately before it goes out, and the whole sequence
// is abandoned for an account that never came back.

import (
	"fmt"
	"strings"
	"time"

	"flomation.app/automate/api/internal/mailer"
	"flomation.app/automate/api/internal/onboardingemail"
	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// OnboardingEmailPersistence is the narrow slice of persistence this poller
// touches, following MarketingSyncPoller above it.
type OnboardingEmailPersistence interface {
	ListDueOnboardingEmails(limit int) ([]persistence.OnboardingEmailDue, error)
	ClaimOnboardingEmail(userID, emailKey string, maxAttempts int) (bool, error)
	MarkOnboardingEmailSent(userID, emailKey string) error
	MarkOnboardingEmailSkipped(userID, emailKey, reason string) error
	SkipRemainingOnboardingEmails(userID, reason string) error
	RecordOnboardingEmailError(userID, emailKey, reason string) error
	BackfillOnboardingEmails() (int, error)

	HasAnyFlow(userID string) (bool, error)
	HasAnyAgent(userID string) (bool, error)
	HasTeam(userID string) (bool, error)
}

const (
	onboardingEmailInterval = 5 * time.Minute
	onboardingEmailBatch    = 25
	// onboardingEmailMaxAttempts bounds retries. A permanently bad address
	// would otherwise be retried every five minutes for ever, and the useful
	// signal (it failed three times, here is the last reason) is already
	// recorded on the row by then.
	onboardingEmailMaxAttempts = 3
)

// OnboardingEmailPoller sends the sequence.
//
// Cadence is five minutes. The first email is due an hour after sign-up and
// the others days later, so minute-level precision buys nothing, and a slow
// loop keeps the decision queries off the database.
type OnboardingEmailPoller struct {
	persistence OnboardingEmailPersistence
	mailer      *mailer.Mailer
	appURL      string
	apiURL      string
	interval    time.Duration
	batchSize   int
}

// StartOnboardingEmailPoller starts the goroutine, or explains why it did not.
//
// Refusing to start without an API URL is deliberate. That URL is what makes
// the unsubscribe link work, and an onboarding email with a broken unsubscribe
// link is worse than no onboarding email: it is the one thing a recipient must
// always be able to do.
func StartOnboardingEmailPoller(p OnboardingEmailPersistence, m *mailer.Mailer, appURL, apiURL string) *OnboardingEmailPoller {
	if !m.Configured() {
		log.Info("onboarding email poller: SMTP not configured, skipping start")
		return nil
	}
	if strings.TrimSpace(apiURL) == "" {
		log.Warn("onboarding email poller: no public API URL configured, so unsubscribe links cannot be built; not starting")
		return nil
	}
	if strings.TrimSpace(appURL) == "" {
		log.Warn("onboarding email poller: no app URL configured; not starting")
		return nil
	}

	op := &OnboardingEmailPoller{
		persistence: p,
		mailer:      m,
		appURL:      strings.TrimRight(appURL, "/"),
		apiURL:      strings.TrimRight(apiURL, "/"),
		interval:    onboardingEmailInterval,
		batchSize:   onboardingEmailBatch,
	}
	go op.watch()
	return op
}

func (op *OnboardingEmailPoller) watch() {
	time.Sleep(30 * time.Second)

	// Enrol accounts that predate the sequence. This runs here rather than as
	// a migration so it uses the same schedule the rest of the code does, and
	// so it cannot enrol anybody in an environment where the poller itself
	// declined to start. It is a no-op after the first successful run.
	if enrolled, err := op.persistence.BackfillOnboardingEmails(); err != nil {
		log.WithError(err).Warn("onboarding email poller: backfill of existing accounts failed; will retry on next start")
	} else if enrolled > 0 {
		// Nothing sends for an hour, which is the window to notice a problem.
		log.WithField("accounts", enrolled).Info("onboarding email poller: enrolled existing accounts")
	}

	ticker := time.NewTicker(op.interval)
	defer ticker.Stop()

	log.WithField("interval", op.interval.String()).Info("onboarding email poller started")

	for range ticker.C {
		op.sweep()
	}
}

func (op *OnboardingEmailPoller) sweep() {
	rows, err := op.persistence.ListDueOnboardingEmails(op.batchSize)
	if err != nil {
		log.WithError(err).Warn("onboarding email poller: failed to list due emails")
		return
	}
	for _, row := range rows {
		op.process(row)
	}
}

// process handles one due row: decide, claim, send.
func (op *OnboardingEmailPoller) process(row persistence.OnboardingEmailDue) {
	l := log.WithFields(log.Fields{
		"user_id":   row.UserID,
		"email_key": row.EmailKey,
	})

	email, ok := onboardingemail.Find(row.EmailKey)
	if !ok {
		// A key in the table that the code no longer knows about. Skip it
		// rather than leaving it due for ever, and say so.
		l.Warn("onboarding email poller: unknown email key, skipping")
		if err := op.persistence.MarkOnboardingEmailSkipped(row.UserID, row.EmailKey, "unknown email key"); err != nil {
			l.WithError(err).Warn("onboarding email poller: failed to skip unknown key")
		}
		return
	}

	// Somebody who signed up and never returned should not be followed by a
	// week of instructions. last_activity_at is only stamped by real use, so
	// its absence after the first email is due means the account was opened
	// and abandoned.
	//
	// The first email is exempt: it falls due an hour in, which is easily
	// within one sitting, and it is the one email a person who has not come
	// back might actually act on.
	if email.Key != onboardingemail.KeyFirstFlow && !returnedAfterSignup(row) {
		l.Info("onboarding email poller: account never returned, abandoning sequence")
		if err := op.persistence.SkipRemainingOnboardingEmails(row.UserID, "no activity since sign-up"); err != nil {
			l.WithError(err).Warn("onboarding email poller: failed to abandon sequence")
		}
		return
	}

	done, err := op.goalAlreadyMet(email.Goal, row.UserID)
	if err != nil {
		l.WithError(err).Warn("onboarding email poller: failed to check whether the goal was already met")
		return
	}
	if done {
		// Not a failure — the reader got there on their own, which is the
		// outcome the email was for.
		l.Debug("onboarding email poller: goal already met, skipping")
		if err := op.persistence.MarkOnboardingEmailSkipped(row.UserID, row.EmailKey, "goal already met"); err != nil {
			l.WithError(err).Warn("onboarding email poller: failed to skip met goal")
		}
		return
	}

	if row.EmailAddress == nil || *row.EmailAddress == "" {
		// The due query filters on a non-null column, so this means decryption
		// produced nothing. Worth a log rather than a silent skip.
		l.Warn("onboarding email poller: no usable email address")
		if err := op.persistence.MarkOnboardingEmailSkipped(row.UserID, row.EmailKey, "no email address"); err != nil {
			l.WithError(err).Warn("onboarding email poller: failed to skip missing address")
		}
		return
	}
	if row.Token == nil {
		l.Warn("onboarding email poller: no unsubscribe token; refusing to send")
		return
	}

	// Claim before sending. Two API instances reach this line for the same row
	// routinely; exactly one of them gets true.
	claimed, err := op.persistence.ClaimOnboardingEmail(row.UserID, row.EmailKey, onboardingEmailMaxAttempts)
	if err != nil {
		l.WithError(err).Warn("onboarding email poller: failed to claim")
		return
	}
	if !claimed {
		return
	}

	rendered := onboardingemail.Render(email, onboardingemail.Links{
		AppURL:         op.appURL,
		UnsubscribeURL: op.unsubscribeURL(row.Token.String()),
		Greeting:       Greeting(row.Name),
	})

	err = op.mailer.Send(mailer.Message{
		To:             *row.EmailAddress,
		Subject:        rendered.Subject,
		HTML:           rendered.HTML,
		Text:           rendered.Text,
		UnsubscribeURL: rendered.UnsubscribeURL,
	})
	if err != nil {
		l.WithError(err).Warn("onboarding email poller: send failed")
		if err := op.persistence.RecordOnboardingEmailError(row.UserID, row.EmailKey, err.Error()); err != nil {
			l.WithError(err).Warn("onboarding email poller: failed to record error")
		}
		return
	}

	if err := op.persistence.MarkOnboardingEmailSent(row.UserID, row.EmailKey); err != nil {
		// The email has gone. Failing to record that is the one bad outcome
		// here, because the attempt counter is the only thing then stopping a
		// resend — which is exactly what it is for.
		l.WithError(err).Error("onboarding email poller: sent but failed to record; attempt counter will stop a resend")
		return
	}

	l.Info("onboarding email poller: sent")
}

func (op *OnboardingEmailPoller) goalAlreadyMet(goal onboardingemail.Goal, userID string) (bool, error) {
	switch goal {
	case onboardingemail.GoalCreateFlow:
		return op.persistence.HasAnyFlow(userID)
	case onboardingemail.GoalCreateAgent:
		return op.persistence.HasAnyAgent(userID)
	case onboardingemail.GoalInviteTeam:
		return op.persistence.HasTeam(userID)
	default:
		return false, fmt.Errorf("unknown onboarding goal %d", goal)
	}
}

func (op *OnboardingEmailPoller) unsubscribeURL(token string) string {
	return op.apiURL + "/api/v1/onboarding/email/unsubscribe/" + token
}

// returnedAfterSignup reports whether the account has been used since it was
// created.
//
// The comparison needs a margin. Provisioning stamps created_at and the first
// request stamps last_activity_at moments apart, so an account that was opened
// and abandoned still has an activity timestamp a second or two after
// creation. Anything inside five minutes is the sign-up itself.
func returnedAfterSignup(row persistence.OnboardingEmailDue) bool {
	if row.LastActivity == nil {
		return false
	}
	return row.LastActivity.After(row.CreatedAt.Add(5 * time.Minute))
}

// Greeting turns a stored name into something safe to open an email with.
//
// users.name is the literal string "auto-generate" until somebody sets a real
// one — 25 of 69 live accounts carried that value when the personal DPA work
// looked — and it is also where an email address ends up for accounts seeded
// from their identity. Neither belongs after the word "Hi", so both give an
// empty greeting and the email opens without one.
func Greeting(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "auto-generate" || strings.Contains(name, "@") {
		return ""
	}
	// First name only. "Hi Grace" reads like a person wrote it; "Hi Grace
	// Beckett" reads like a mail merge.
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}
