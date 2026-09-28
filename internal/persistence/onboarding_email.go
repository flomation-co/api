package persistence

// Storage for the post-registration onboarding email sequence.
//
// Two rules shape everything here:
//
//  1. Enrolment is explicit. Rows are written once, when the product first
//     sees an account, and the due times are frozen at that moment. Nothing
//     derives a schedule from users.created_at, because that would enrol
//     every existing account the day this ships.
//
//  2. A row is claimed before it is sent, not after. The API runs on more
//     than one instance and every one of them polls, so "select due, send,
//     mark sent" would send some emails twice. ClaimOnboardingEmail does the
//     claim as a single conditional UPDATE and the sender only proceeds if it
//     got the row.

import (
	"database/sql"
	"errors"
	"time"

	"flomation.app/automate/api/internal/onboardingemail"
	"github.com/google/uuid"
)

// OnboardingEmailDue is one email ready to be considered for sending, with
// everything the poller needs to decide and to address it.
type OnboardingEmailDue struct {
	UserID       string     `db:"user_id"`
	EmailKey     string     `db:"email_key"`
	Attempts     int        `db:"attempts"`
	Name         string     `db:"name"`
	EmailAddress *string    `db:"email_address"`
	Token        *uuid.UUID `db:"onboarding_email_token"`
	CreatedAt    time.Time  `db:"created_at"`
	LastActivity *time.Time `db:"last_activity_at"`
}

// EnrolOnboardingEmails writes the schedule for a newly provisioned account
// and gives it an unsubscribe token.
//
// Idempotent on both halves: ON CONFLICT DO NOTHING on the schedule rows, and
// the token is only generated when there isn't one. Provisioning is a
// one-shot path, but it is reached from a request handler, and two concurrent
// first requests from the same new account are entirely possible.
//
// Errors are returned rather than swallowed, but the caller is expected to log
// and carry on: failing to schedule an email must never fail the request that
// created the account.
func (s *Service) EnrolOnboardingEmails(userID string, enrolledAt time.Time) error {
	due := onboardingemail.Schedule(enrolledAt)

	tx, err := s.conn.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for key, at := range due {
		if _, err := tx.Exec(
			`INSERT INTO user_onboarding_email (user_id, email_key, due_at)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (user_id, email_key) DO NOTHING`,
			userID, key, at,
		); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(
		`UPDATE users SET onboarding_email_token = gen_random_uuid()
		 WHERE id = $1 AND onboarding_email_token IS NULL`,
		userID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// ListDueOnboardingEmails returns rows that are due and undecided, for users
// who have not opted out and for whom we hold an email address.
//
// Opt-out and a missing address are filtered here rather than checked per-row
// by the caller so that a user who opted out never appears in the poller's
// logs at all.
func (s *Service) ListDueOnboardingEmails(limit int) ([]OnboardingEmailDue, error) {
	var rows []OnboardingEmailDue
	// email_address is encrypted at rest, same as everywhere else it is read.
	err := s.conn.Select(&rows,
		`SELECT oe.user_id,
		        oe.email_key,
		        oe.attempts,
		        u.name,
		        PGP_SYM_DECRYPT(u.email_address, $2) AS email_address,
		        u.onboarding_email_token,
		        u.created_at,
		        u.last_activity_at
		 FROM user_onboarding_email oe
		 JOIN users u ON u.id = oe.user_id
		 WHERE oe.sent_at IS NULL
		   AND oe.skipped_at IS NULL
		   AND oe.due_at <= NOW()
		   AND u.onboarding_email_opt_out_at IS NULL
		   AND u.email_address IS NOT NULL
		 ORDER BY oe.due_at
		 LIMIT $1`,
		limit, s.config.Database.EncryptionKey)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ClaimOnboardingEmail takes exclusive ownership of a row for one send
// attempt, returning false if another instance got there first.
//
// The attempt counter is bumped by the claim itself, so a process that dies
// mid-send does not leave a row that can be retried for ever.
func (s *Service) ClaimOnboardingEmail(userID, emailKey string, maxAttempts int) (bool, error) {
	res, err := s.conn.Exec(
		`UPDATE user_onboarding_email
		 SET attempts = attempts + 1
		 WHERE user_id = $1 AND email_key = $2
		   AND sent_at IS NULL AND skipped_at IS NULL
		   AND attempts < $3`,
		userID, emailKey, maxAttempts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkOnboardingEmailSent records a successful delivery.
func (s *Service) MarkOnboardingEmailSent(userID, emailKey string) error {
	_, err := s.conn.Exec(
		`UPDATE user_onboarding_email
		 SET sent_at = NOW(), last_error = NULL
		 WHERE user_id = $1 AND email_key = $2`,
		userID, emailKey)
	return err
}

// MarkOnboardingEmailSkipped records that an email was deliberately not sent,
// and why. The reason is the whole point: "skipped" with no reason cannot be
// told apart from a bug.
func (s *Service) MarkOnboardingEmailSkipped(userID, emailKey, reason string) error {
	_, err := s.conn.Exec(
		`UPDATE user_onboarding_email
		 SET skipped_at = NOW(), skip_reason = $3
		 WHERE user_id = $1 AND email_key = $2
		   AND sent_at IS NULL AND skipped_at IS NULL`,
		userID, emailKey, reason)
	return err
}

// SkipRemainingOnboardingEmails cancels everything still outstanding for a
// user. Used when the sequence should stop as a whole — an account that never
// came back, or one that opted out.
func (s *Service) SkipRemainingOnboardingEmails(userID, reason string) error {
	_, err := s.conn.Exec(
		`UPDATE user_onboarding_email
		 SET skipped_at = NOW(), skip_reason = $2
		 WHERE user_id = $1 AND sent_at IS NULL AND skipped_at IS NULL`,
		userID, reason)
	return err
}

// RecordOnboardingEmailError stores the last failure without deciding the
// row's fate. Whether it is retried is governed by the attempt counter the
// claim already incremented.
func (s *Service) RecordOnboardingEmailError(userID, emailKey, reason string) error {
	_, err := s.conn.Exec(
		`UPDATE user_onboarding_email SET last_error = $3
		 WHERE user_id = $1 AND email_key = $2`,
		userID, emailKey, reason)
	return err
}

// OptOutOfOnboardingEmails is what the unsubscribe link does. Returns false
// when the token matches nobody, so the handler can answer identically either
// way rather than confirming which tokens are real.
func (s *Service) OptOutOfOnboardingEmails(token string) (bool, error) {
	parsed, err := uuid.Parse(token)
	if err != nil {
		return false, nil
	}

	var userID string
	err = s.conn.Get(&userID,
		`UPDATE users SET onboarding_email_opt_out_at = COALESCE(onboarding_email_opt_out_at, NOW())
		 WHERE onboarding_email_token = $1
		 RETURNING id`,
		parsed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	// Cancel what is outstanding as well as setting the flag. The flag alone
	// would be enough while the poller checks it, but leaving rows due means
	// any future code path that reads "due" has to remember to check the flag
	// too.
	if err := s.SkipRemainingOnboardingEmails(userID, "unsubscribed"); err != nil {
		return true, err
	}
	return true, nil
}

// HasAnyFlow reports whether the user owns a flow, in any organisation.
func (s *Service) HasAnyFlow(userID string) (bool, error) {
	return s.existsForUser(`SELECT EXISTS (SELECT 1 FROM flo WHERE author_id = $1)`, userID)
}

// HasAnyAgent reports whether the user owns an agent that has not been
// archived. An archived agent still counts as having learnt the feature, so
// archived_at is deliberately not filtered.
func (s *Service) HasAnyAgent(userID string) (bool, error) {
	return s.existsForUser(`SELECT EXISTS (SELECT 1 FROM agent WHERE owner_id = $1)`, userID)
}

// HasTeam reports whether the user has anybody to work with: an invite they
// sent, or an organisation they share with another person.
//
// Both halves matter. Somebody who was invited into an existing team has a
// team without ever having sent an invite, and telling them to invite their
// team would be absurd.
func (s *Service) HasTeam(userID string) (bool, error) {
	return s.existsForUser(
		`SELECT EXISTS (SELECT 1 FROM organisation_invite WHERE created_by = $1)
		     OR EXISTS (
		        SELECT 1
		        FROM organisation_user mine
		        JOIN organisation_user theirs
		          ON theirs.organisation_id = mine.organisation_id
		         AND theirs.user_id <> mine.user_id
		        WHERE mine.user_id = $1
		     )`, userID)
}

func (s *Service) existsForUser(query, userID string) (bool, error) {
	var exists bool
	if err := s.conn.Get(&exists, query, userID); err != nil {
		return false, err
	}
	return exists, nil
}
