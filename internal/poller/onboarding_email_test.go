package poller

import (
	"errors"
	"testing"
	"time"

	"flomation.app/automate/api/internal/mailer"
	"flomation.app/automate/api/internal/onboardingemail"
	"flomation.app/automate/api/internal/persistence"
	"github.com/google/uuid"
	. "github.com/onsi/gomega"
)

type onboardingMock struct {
	hasFlow, hasAgent, hasTeam bool
	checkErr                   error

	claimed   bool
	claimCall int

	sent      []string
	skipped   map[string]string
	abandoned string
	errors    map[string]string
}

func newOnboardingMock() *onboardingMock {
	return &onboardingMock{
		claimed: true,
		skipped: map[string]string{},
		errors:  map[string]string{},
	}
}

func (m *onboardingMock) ListDueOnboardingEmails(int) ([]persistence.OnboardingEmailDue, error) {
	return nil, nil
}

func (m *onboardingMock) ClaimOnboardingEmail(_, _ string, _ int) (bool, error) {
	m.claimCall++
	return m.claimed, nil
}

func (m *onboardingMock) MarkOnboardingEmailSent(_, key string) error {
	m.sent = append(m.sent, key)
	return nil
}

func (m *onboardingMock) MarkOnboardingEmailSkipped(_, key, reason string) error {
	m.skipped[key] = reason
	return nil
}

func (m *onboardingMock) SkipRemainingOnboardingEmails(_, reason string) error {
	m.abandoned = reason
	return nil
}

func (m *onboardingMock) RecordOnboardingEmailError(_, key, reason string) error {
	m.errors[key] = reason
	return nil
}

func (m *onboardingMock) HasAnyFlow(string) (bool, error)  { return m.hasFlow, m.checkErr }
func (m *onboardingMock) HasAnyAgent(string) (bool, error) { return m.hasAgent, m.checkErr }
func (m *onboardingMock) HasTeam(string) (bool, error)     { return m.hasTeam, m.checkErr }

func testPoller(m *onboardingMock) *OnboardingEmailPoller {
	return &OnboardingEmailPoller{
		persistence: m,
		// Unconfigured: Send returns an error rather than reaching the
		// network, which is what lets these tests assert on the decision
		// without an SMTP server.
		mailer: mailer.New(mailer.Config{}),
		appURL: "https://app.example.com",
		apiURL: "https://api.example.com",
	}
}

func dueRow(key string, created time.Time, activity *time.Time) persistence.OnboardingEmailDue {
	addr := "grace@example.com"
	token := uuid.New()
	return persistence.OnboardingEmailDue{
		UserID: "11112222-3333-4444-5555-666677778888", EmailKey: key,
		Name: "Grace Beckett", EmailAddress: &addr, Token: &token,
		CreatedAt: created, LastActivity: activity,
	}
}

func active(base time.Time) *time.Time { t := base.Add(2 * time.Hour); return &t }

// The whole point of the poller: an email whose lesson has already been
// learnt is not sent.
func TestGoalAlreadyMetIsSkippedNotSent(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-8 * 24 * time.Hour)
	cases := []struct {
		key  string
		meet func(*onboardingMock)
	}{
		{onboardingemail.KeyFirstFlow, func(m *onboardingMock) { m.hasFlow = true }},
		{onboardingemail.KeyFirstAgent, func(m *onboardingMock) { m.hasAgent = true }},
		{onboardingemail.KeyInviteTeam, func(m *onboardingMock) { m.hasTeam = true }},
	}

	for _, tc := range cases {
		m := newOnboardingMock()
		tc.meet(m)
		testPoller(m).process(dueRow(tc.key, base, active(base)))

		Expect(m.skipped).To(HaveKeyWithValue(tc.key, "goal already met"), "key %s", tc.key)
		Expect(m.sent).To(BeEmpty(), "key %s", tc.key)
		Expect(m.claimCall).To(BeZero(), "key %s must not be claimed if it is not going out", tc.key)
	}
}

// An account opened and never returned to should not be followed for a week.
func TestSequenceIsAbandonedForAnAccountThatNeverCameBack(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-8 * 24 * time.Hour)
	m := newOnboardingMock()
	testPoller(m).process(dueRow(onboardingemail.KeyFirstAgent, base, nil))

	Expect(m.abandoned).To(Equal("no activity since sign-up"))
	Expect(m.claimCall).To(BeZero())
}

// Activity stamped moments after creation is the sign-up itself, not a return
// visit. Without a margin every abandoned account looks active.
func TestActivityDuringSignupDoesNotCountAsReturning(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-8 * 24 * time.Hour)
	duringSignup := base.Add(30 * time.Second)

	m := newOnboardingMock()
	testPoller(m).process(dueRow(onboardingemail.KeyFirstAgent, base, &duringSignup))
	Expect(m.abandoned).To(Equal("no activity since sign-up"))

	m2 := newOnboardingMock()
	testPoller(m2).process(dueRow(onboardingemail.KeyFirstAgent, base, active(base)))
	Expect(m2.abandoned).To(BeEmpty())
}

// The first email falls due an hour in, well within one sitting, and is the
// one a person who has not returned might still act on.
func TestFirstEmailIsExemptFromTheActivityCheck(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-2 * time.Hour)
	m := newOnboardingMock()
	testPoller(m).process(dueRow(onboardingemail.KeyFirstFlow, base, nil))

	Expect(m.abandoned).To(BeEmpty())
	Expect(m.claimCall).To(Equal(1), "it should have been claimed and attempted")
}

func TestUnknownKeyIsSkippedRatherThanLeftDueForEver(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-8 * 24 * time.Hour)
	m := newOnboardingMock()
	testPoller(m).process(dueRow("retired_email", base, active(base)))

	Expect(m.skipped).To(HaveKeyWithValue("retired_email", "unknown email key"))
}

// Losing the claim means another instance is sending it. Doing anything else
// here is how a person gets the same email twice.
func TestLosingTheClaimSendsNothing(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-2 * time.Hour)
	m := newOnboardingMock()
	m.claimed = false
	testPoller(m).process(dueRow(onboardingemail.KeyFirstFlow, base, nil))

	Expect(m.sent).To(BeEmpty())
	Expect(m.errors).To(BeEmpty())
	Expect(m.skipped).To(BeEmpty())
}

// A failed send must not be marked sent — the attempt counter, bumped by the
// claim, is what eventually stops it being retried.
func TestFailedSendIsRecordedNotMarkedSent(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-2 * time.Hour)
	m := newOnboardingMock()
	testPoller(m).process(dueRow(onboardingemail.KeyFirstFlow, base, nil))

	Expect(m.sent).To(BeEmpty())
	Expect(m.errors).To(HaveKey(onboardingemail.KeyFirstFlow))
	Expect(m.errors[onboardingemail.KeyFirstFlow]).To(ContainSubstring("not configured"))
}

// A failure to answer "have they already done this?" must not be read as no.
// Guessing wrong here sends a person instructions for something they did.
func TestAFailedGoalCheckDefersRatherThanSends(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-2 * time.Hour)
	m := newOnboardingMock()
	m.checkErr = errors.New("database is down")
	testPoller(m).process(dueRow(onboardingemail.KeyFirstFlow, base, nil))

	Expect(m.claimCall).To(BeZero())
	Expect(m.sent).To(BeEmpty())
	Expect(m.skipped).To(BeEmpty(), "the row must stay due so it is retried")
}

func TestAMissingTokenStopsTheSend(t *testing.T) {
	RegisterTestingT(t)

	base := time.Now().Add(-2 * time.Hour)
	row := dueRow(onboardingemail.KeyFirstFlow, base, nil)
	row.Token = nil

	m := newOnboardingMock()
	testPoller(m).process(row)

	// No token means no unsubscribe link, and an email nobody can stop is
	// worse than an email nobody gets.
	Expect(m.claimCall).To(BeZero())
	Expect(m.sent).To(BeEmpty())
}

func TestUnsubscribeURLIsBuiltFromThePublicAPIURL(t *testing.T) {
	RegisterTestingT(t)

	p := testPoller(newOnboardingMock())
	Expect(p.unsubscribeURL("tok-123")).To(
		Equal("https://api.example.com/api/v1/onboarding/email/unsubscribe/tok-123"))
}

func TestGreeting(t *testing.T) {
	RegisterTestingT(t)

	Expect(Greeting("Grace Beckett")).To(Equal("Grace"))
	Expect(Greeting("gracieb")).To(Equal("gracieb"))
	Expect(Greeting("  Grace  ")).To(Equal("Grace"))

	// The placeholder, an email address and nothing at all all produce no
	// greeting rather than "Hi auto-generate".
	Expect(Greeting("auto-generate")).To(BeEmpty())
	Expect(Greeting("grace@example.com")).To(BeEmpty())
	Expect(Greeting("")).To(BeEmpty())
	Expect(Greeting("   ")).To(BeEmpty())
}

func TestPollerRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	RegisterTestingT(t)

	configured := mailer.New(mailer.Config{Host: "h", From: "a@b.c"})
	m := newOnboardingMock()

	Expect(StartOnboardingEmailPoller(m, mailer.New(mailer.Config{}), "https://app", "https://api")).
		To(BeNil(), "no SMTP")
	// Without a public API URL the unsubscribe link cannot be built, and an
	// onboarding email that cannot be stopped is not one to send.
	Expect(StartOnboardingEmailPoller(m, configured, "https://app", "")).
		To(BeNil(), "no API URL")
	Expect(StartOnboardingEmailPoller(m, configured, "", "https://api")).
		To(BeNil(), "no app URL")
}
