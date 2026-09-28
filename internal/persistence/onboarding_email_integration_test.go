package persistence

// Exercises the onboarding email storage against a real PostgreSQL.
//
// The rest of the persistence package is tested with sqlmock, which asserts
// that a query was issued but says nothing about whether PostgreSQL would
// accept it. That is the wrong tool here, because everything worth getting
// wrong in this file is SQL the database has to agree with: an ON CONFLICT
// against a composite primary key, a partial index, a conditional UPDATE used
// as a lock, and a pgp_sym_decrypt of a column written by a different
// statement.
//
// Skipped unless FLOMATION_TEST_POSTGRES_DSN points at a database this test
// may create and drop schemas in. Locally:
//
//	FLOMATION_TEST_POSTGRES_DSN='postgres://postgres:Password1234@localhost:5432/postgres?sslmode=disable' \
//	  go test ./internal/persistence/ -run Onboarding

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/onboardingemail"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	. "github.com/onsi/gomega"
)

const testEncryptionKey = "Test1234!"

// onboardingTestDB brings up a throwaway database with the real migration set
// applied — not a hand-written schema, which would be free to disagree with
// what actually ships.
func onboardingTestDB(t *testing.T) *Service {
	t.Helper()

	dsn := os.Getenv("FLOMATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FLOMATION_TEST_POSTGRES_DSN to run onboarding email storage tests")
	}

	admin, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("cannot reach PostgreSQL: %v", err)
	}
	defer func() { _ = admin.Close() }()

	name := "onboarding_test_" + uuid.New().String()[:8]
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("cannot create test database: %v", err)
	}
	t.Cleanup(func() {
		a, err := sqlx.Connect("postgres", dsn)
		if err != nil {
			return
		}
		defer func() { _ = a.Close() }()
		_, _ = a.Exec(fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name))
	})

	testDSN := replaceDatabase(dsn, name)

	db, err := sqlx.Connect("postgres", testDSN)
	if err != nil {
		t.Fatalf("cannot connect to test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	src, err := iofs.New(migrations, "migration")
	if err != nil {
		t.Fatalf("cannot load migrations: %v", err)
	}
	driver, err := postgres.WithInstance(db.DB, &postgres.Config{})
	if err != nil {
		t.Fatalf("cannot build migrate driver: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		t.Fatalf("cannot build migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrations failed: %v", err)
	}

	return &Service{
		config: &config.Config{Database: config.DatabaseConfig{EncryptionKey: testEncryptionKey}},
		conn:   db.Unsafe(),
	}
}

func replaceDatabase(dsn, name string) string {
	// postgres://user:pass@host:port/dbname?params
	slash := -1
	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '/' {
			slash = i
			break
		}
	}
	rest := ""
	for i := slash; i < len(dsn); i++ {
		if dsn[i] == '?' {
			rest = dsn[i:]
			break
		}
	}
	return dsn[:slash+1] + name + rest
}

// makeUser inserts a user the way the application does, so email_address is
// encrypted with the same key the read path uses.
func makeUser(t *testing.T, s *Service, name, email string) string {
	t.Helper()
	var id string
	err := s.conn.Get(&id,
		`INSERT INTO users (name, email_address, created_at)
		 VALUES ($1, PGP_SYM_ENCRYPT($2, $3), NOW()) RETURNING id`,
		name, email, testEncryptionKey)
	if err != nil {
		t.Fatalf("cannot create user: %v", err)
	}
	return id
}

func TestOnboardingEnrolmentIsIdempotent(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	at := time.Now().Add(-8 * 24 * time.Hour)

	Expect(s.EnrolOnboardingEmails(userID, at)).To(Succeed())
	// Two concurrent first requests from one new account are ordinary, so a
	// second enrolment must change nothing rather than error.
	Expect(s.EnrolOnboardingEmails(userID, time.Now())).To(Succeed())

	var count int
	Expect(s.conn.Get(&count,
		`SELECT count(*) FROM user_onboarding_email WHERE user_id = $1`, userID)).To(Succeed())
	Expect(count).To(Equal(len(onboardingemail.Sequence)))

	// The schedule is frozen at first enrolment. Re-enrolling must not push
	// the due times forward.
	var due time.Time
	Expect(s.conn.Get(&due,
		`SELECT due_at FROM user_onboarding_email WHERE user_id = $1 AND email_key = $2`,
		userID, onboardingemail.KeyFirstFlow)).To(Succeed())
	Expect(due).To(BeTemporally("~", at.Add(time.Hour), time.Second))

	var token *uuid.UUID
	Expect(s.conn.Get(&token,
		`SELECT onboarding_email_token FROM users WHERE id = $1`, userID)).To(Succeed())
	Expect(token).ToNot(BeNil())
}

func TestOnboardingDueQueryReturnsDecryptedAddresses(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(HaveLen(3))

	for _, r := range rows {
		Expect(r.EmailAddress).ToNot(BeNil())
		Expect(*r.EmailAddress).To(Equal("grace@example.com"))
		Expect(r.Token).ToNot(BeNil())
		Expect(r.Name).To(Equal("Grace"))
	}
}

func TestOnboardingDueQueryExcludesWhatItShould(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	past := time.Now().Add(-8 * 24 * time.Hour)

	optedOut := makeUser(t, s, "Opted out", "out@example.com")
	Expect(s.EnrolOnboardingEmails(optedOut, past)).To(Succeed())
	_, err := s.conn.Exec(`UPDATE users SET onboarding_email_opt_out_at = NOW() WHERE id = $1`, optedOut)
	Expect(err).ToNot(HaveOccurred())

	noEmail := makeUser(t, s, "No address", "temp@example.com")
	Expect(s.EnrolOnboardingEmails(noEmail, past)).To(Succeed())
	_, err = s.conn.Exec(`UPDATE users SET email_address = NULL WHERE id = $1`, noEmail)
	Expect(err).ToNot(HaveOccurred())

	// Enrolled now, so nothing is due yet.
	future := makeUser(t, s, "Brand new", "new@example.com")
	Expect(s.EnrolOnboardingEmails(future, time.Now())).To(Succeed())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty())
}

// The claim is the only thing standing between several API instances and a
// duplicate send.
func TestOnboardingClaimIsExclusive(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())

	first, err := s.ClaimOnboardingEmail(userID, onboardingemail.KeyFirstFlow, 3)
	Expect(err).ToNot(HaveOccurred())
	Expect(first).To(BeTrue())

	// A second claim succeeds too — it is a retry, not a duplicate, because
	// the first attempt has not been recorded as sent.
	second, err := s.ClaimOnboardingEmail(userID, onboardingemail.KeyFirstFlow, 3)
	Expect(err).ToNot(HaveOccurred())
	Expect(second).To(BeTrue())

	third, err := s.ClaimOnboardingEmail(userID, onboardingemail.KeyFirstFlow, 3)
	Expect(err).ToNot(HaveOccurred())
	Expect(third).To(BeTrue())

	// Beyond the attempt limit it stops, so a permanently undeliverable
	// address is not retried every five minutes for ever.
	fourth, err := s.ClaimOnboardingEmail(userID, onboardingemail.KeyFirstFlow, 3)
	Expect(err).ToNot(HaveOccurred())
	Expect(fourth).To(BeFalse())
}

func TestOnboardingClaimRefusesAnAlreadySentRow(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())
	Expect(s.MarkOnboardingEmailSent(userID, onboardingemail.KeyFirstFlow)).To(Succeed())

	claimed, err := s.ClaimOnboardingEmail(userID, onboardingemail.KeyFirstFlow, 3)
	Expect(err).ToNot(HaveOccurred())
	Expect(claimed).To(BeFalse())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(HaveLen(2), "a sent email must leave the due set")
}

func TestOnboardingSkipRecordsTheReason(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())
	Expect(s.MarkOnboardingEmailSkipped(userID, onboardingemail.KeyFirstFlow, "goal already met")).To(Succeed())

	var reason string
	Expect(s.conn.Get(&reason,
		`SELECT skip_reason FROM user_onboarding_email WHERE user_id = $1 AND email_key = $2`,
		userID, onboardingemail.KeyFirstFlow)).To(Succeed())
	Expect(reason).To(Equal("goal already met"))
}

func TestOnboardingUnsubscribeStopsEverything(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())

	var token string
	Expect(s.conn.Get(&token,
		`SELECT onboarding_email_token FROM users WHERE id = $1`, userID)).To(Succeed())

	found, err := s.OptOutOfOnboardingEmails(token)
	Expect(err).ToNot(HaveOccurred())
	Expect(found).To(BeTrue())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty())

	var outstanding int
	Expect(s.conn.Get(&outstanding,
		`SELECT count(*) FROM user_onboarding_email
		 WHERE user_id = $1 AND sent_at IS NULL AND skipped_at IS NULL`, userID)).To(Succeed())
	Expect(outstanding).To(BeZero(), "outstanding rows must be cancelled, not merely filtered")

	// Clicking the link twice is normal and must not error or move the date.
	var first time.Time
	Expect(s.conn.Get(&first,
		`SELECT onboarding_email_opt_out_at FROM users WHERE id = $1`, userID)).To(Succeed())
	found, err = s.OptOutOfOnboardingEmails(token)
	Expect(err).ToNot(HaveOccurred())
	Expect(found).To(BeTrue())
	var second time.Time
	Expect(s.conn.Get(&second,
		`SELECT onboarding_email_opt_out_at FROM users WHERE id = $1`, userID)).To(Succeed())
	Expect(second).To(BeTemporally("==", first))
}

func TestOnboardingUnsubscribeWithABadToken(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	// Not a UUID at all — must be a clean "no" rather than a database error,
	// because this endpoint is public and takes whatever is in the URL.
	found, err := s.OptOutOfOnboardingEmails("not-a-uuid")
	Expect(err).ToNot(HaveOccurred())
	Expect(found).To(BeFalse())

	// A well-formed token belonging to nobody.
	found, err = s.OptOutOfOnboardingEmails(uuid.New().String())
	Expect(err).ToNot(HaveOccurred())
	Expect(found).To(BeFalse())
}

func TestOnboardingGoalChecks(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")

	has, err := s.HasAnyFlow(userID)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeFalse())

	_, err = s.conn.Exec(`INSERT INTO flo (name, author_id) VALUES ('First', $1)`, userID)
	Expect(err).ToNot(HaveOccurred())
	has, err = s.HasAnyFlow(userID)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeTrue())

	has, err = s.HasAnyAgent(userID)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeFalse())

	_, err = s.conn.Exec(`INSERT INTO agent (name, owner_id) VALUES ('Helper', $1)`, userID)
	Expect(err).ToNot(HaveOccurred())
	has, err = s.HasAnyAgent(userID)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeTrue())
}

// Somebody invited into an existing team already has a team. Telling them to
// invite one would be absurd, so both halves of the check matter.
func TestHasTeamCountsBeingInvitedAsWellAsInviting(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	solo := makeUser(t, s, "Solo", "solo@example.com")
	var orgID string
	Expect(s.conn.Get(&orgID,
		`INSERT INTO organisation (name) VALUES ('Acme') RETURNING id`)).To(Succeed())
	_, err := s.conn.Exec(
		`INSERT INTO organisation_user (organisation_id, user_id) VALUES ($1, $2)`, orgID, solo)
	Expect(err).ToNot(HaveOccurred())

	// Alone in an organisation is not a team.
	has, err := s.HasTeam(solo)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeFalse())

	colleague := makeUser(t, s, "Colleague", "colleague@example.com")
	_, err = s.conn.Exec(
		`INSERT INTO organisation_user (organisation_id, user_id) VALUES ($1, $2)`, orgID, colleague)
	Expect(err).ToNot(HaveOccurred())

	// Both of them now have a team, including the one who sent no invite.
	for _, id := range []string{solo, colleague} {
		has, err = s.HasTeam(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(has).To(BeTrue())
	}
}

func TestHasTeamCountsAPendingInvite(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	inviter := makeUser(t, s, "Inviter", "inviter@example.com")
	var orgID string
	Expect(s.conn.Get(&orgID,
		`INSERT INTO organisation (name) VALUES ('Acme') RETURNING id`)).To(Succeed())

	// An invite that has been sent but not yet accepted still means the
	// lesson has landed.
	_, err := s.conn.Exec(
		`INSERT INTO organisation_invite (organisation_id, email, created_by)
		 VALUES ($1, 'someone@example.com', $2)`, orgID, inviter)
	Expect(err).ToNot(HaveOccurred())

	has, err := s.HasTeam(inviter)
	Expect(err).ToNot(HaveOccurred())
	Expect(has).To(BeTrue())
}

func TestOnboardingRowsGoWithTheUser(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now())).To(Succeed())

	_, err := s.conn.Exec(`DELETE FROM users WHERE id = $1`, userID)
	Expect(err).ToNot(HaveOccurred(), "the schedule must not pin a user row in place")

	var count int
	Expect(s.conn.Get(&count,
		`SELECT count(*) FROM user_onboarding_email WHERE user_id = $1`, userID)).To(Succeed())
	Expect(count).To(BeZero())
}

var _ = sql.ErrNoRows

// makeAnonymousUser inserts the stub row that an unrecognised channel
// identity produces — a Slack person who messaged somebody's agent, not an
// account holder.
func makeAnonymousUser(t *testing.T, s *Service, email string) string {
	t.Helper()
	var orgID string
	if err := s.conn.Get(&orgID,
		`INSERT INTO organisation (name) VALUES ('Acme') RETURNING id`); err != nil {
		t.Fatalf("cannot create organisation: %v", err)
	}
	var id string
	err := s.conn.Get(&id,
		`INSERT INTO users (name, email_address, is_anonymous,
		                    organisation_id, channel_type, channel_external_id)
		 VALUES ('slack-person', PGP_SYM_ENCRYPT($1, $2), true, $3, 'slack', 'U123')
		 RETURNING id`, email, testEncryptionKey, orgID)
	if err != nil {
		t.Fatalf("cannot create anonymous user: %v", err)
	}
	return id
}

func TestOnboardingBackfillEnrolsExistingAccounts(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	a := makeUser(t, s, "Grace", "grace@example.com")
	b := makeUser(t, s, "Ada", "ada@example.com")

	n, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(n).To(Equal(2))

	for _, id := range []string{a, b} {
		var count int
		Expect(s.conn.Get(&count,
			`SELECT count(*) FROM user_onboarding_email WHERE user_id = $1`, id)).To(Succeed())
		Expect(count).To(Equal(len(onboardingemail.Sequence)))
	}
}

// Backdating would make all three due at once and deliver the whole sequence
// in a single sweep. The spacing only means anything measured forward.
func TestOnboardingBackfillSchedulesFromNowNotFromSignup(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	_, err := s.conn.Exec(
		`UPDATE users SET created_at = NOW() - INTERVAL '2 years' WHERE id = $1`, userID)
	Expect(err).ToNot(HaveOccurred())

	_, err = s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())

	// Nothing is due yet — not even the first one, which is an hour out.
	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty())

	var due time.Time
	Expect(s.conn.Get(&due,
		`SELECT due_at FROM user_onboarding_email WHERE user_id = $1 AND email_key = $2`,
		userID, onboardingemail.KeyInviteTeam)).To(Succeed())
	Expect(due).To(BeTemporally(">", time.Now().Add(6*24*time.Hour)))
}

func TestOnboardingBackfillIsSafeToRunTwice(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	makeUser(t, s, "Grace", "grace@example.com")

	first, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(first).To(Equal(1))

	// Every API instance runs this on start, and it runs on every restart.
	second, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(second).To(BeZero())

	var count int
	Expect(s.conn.Get(&count, `SELECT count(*) FROM user_onboarding_email`)).To(Succeed())
	Expect(count).To(Equal(len(onboardingemail.Sequence)))
}

// An anonymous row is a stub for a channel identity, not an account. Emailing
// one would mean emailing somebody who never signed up for anything.
func TestOnboardingBackfillSkipsAnonymousRowsAndAccountsWithNoAddress(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	anon := makeAnonymousUser(t, s, "slack-person@example.com")
	noAddress := makeUser(t, s, "No address", "temp@example.com")
	_, err := s.conn.Exec(`UPDATE users SET email_address = NULL WHERE id = $1`, noAddress)
	Expect(err).ToNot(HaveOccurred())
	real := makeUser(t, s, "Grace", "grace@example.com")

	n, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(n).To(Equal(1), "only the real account")

	for _, id := range []string{anon, noAddress} {
		var count int
		Expect(s.conn.Get(&count,
			`SELECT count(*) FROM user_onboarding_email WHERE user_id = $1`, id)).To(Succeed())
		Expect(count).To(BeZero())
	}
	var count int
	Expect(s.conn.Get(&count,
		`SELECT count(*) FROM user_onboarding_email WHERE user_id = $1`, real)).To(Succeed())
	Expect(count).To(Equal(len(onboardingemail.Sequence)))
}

// Even if an anonymous row somehow acquired schedule rows, it must not be
// mailed. The due query is the second of the two guards.
func TestDueQueryExcludesAnonymousRows(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	anon := makeAnonymousUser(t, s, "slack-person@example.com")
	Expect(s.EnrolOnboardingEmails(anon, time.Now().Add(-8*24*time.Hour))).To(Succeed())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty())
}

// An account that unsubscribed, or was already enrolled, is not re-enrolled
// by a later restart.
func TestOnboardingBackfillLeavesDecidedAccountsAlone(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())

	var token string
	Expect(s.conn.Get(&token,
		`SELECT onboarding_email_token FROM users WHERE id = $1`, userID)).To(Succeed())
	_, err := s.OptOutOfOnboardingEmails(token)
	Expect(err).ToNot(HaveOccurred())

	n, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(n).To(BeZero(), "an unsubscribed account keeps its rows and must not be re-enrolled")

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty())
}

// A skipped row never comes back.
//
// SkipRemainingOnboardingEmails is now only reached by unsubscribing — the
// poller skips one email at a time and re-judges the next when it falls due.
// So this is the unsubscribe guarantee: once somebody has opted out, later
// activity on the account does not put them back in the due set, and the
// backfill does not re-enrol them.
func TestUnsubscribedRowsDoNotReviveWhenTheAccountReturns(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())
	Expect(s.MarkOnboardingEmailSent(userID, onboardingemail.KeyFirstFlow)).To(Succeed())
	Expect(s.SkipRemainingOnboardingEmails(userID, "unsubscribed")).To(Succeed())

	// The account comes back and uses the product.
	_, err := s.conn.Exec(`UPDATE users SET last_activity_at = NOW() WHERE id = $1`, userID)
	Expect(err).ToNot(HaveOccurred())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())
	Expect(rows).To(BeEmpty(), "an opt-out must not be undone by using the product")

	n, err := s.BackfillOnboardingEmails()
	Expect(err).ToNot(HaveOccurred())
	Expect(n).To(BeZero(), "the backfill must not re-enrol an account that already has rows")
}

// Skipping one email leaves the others due, which is what lets a reader the
// first email brought back still receive the rest.
func TestSkippingOneEmailLeavesTheOthersDue(t *testing.T) {
	RegisterTestingT(t)
	s := onboardingTestDB(t)

	userID := makeUser(t, s, "Grace", "grace@example.com")
	Expect(s.EnrolOnboardingEmails(userID, time.Now().Add(-8*24*time.Hour))).To(Succeed())
	Expect(s.MarkOnboardingEmailSkipped(userID, onboardingemail.KeyFirstAgent,
		"no activity since sign-up")).To(Succeed())

	rows, err := s.ListDueOnboardingEmails(50)
	Expect(err).ToNot(HaveOccurred())

	keys := []string{}
	for _, r := range rows {
		keys = append(keys, r.EmailKey)
	}
	Expect(keys).To(ConsistOf(onboardingemail.KeyFirstFlow, onboardingemail.KeyInviteTeam))
}
