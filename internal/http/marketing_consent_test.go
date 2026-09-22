package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/connector/identity"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// welcomeMock captures what the welcome/profile handlers hand to persistence,
// so the tests can assert on the consent decision rather than on side effects.
type welcomeMock struct {
	*mockPersistence

	welcomeCalled bool
	gotName       string
	gotOptIn      *bool

	toggleCalled bool
	gotToggle    bool
}

func (m *welcomeMock) CompleteUserWelcome(userID, name string, marketingOptIn *bool) error {
	m.welcomeCalled = true
	m.gotName = name
	m.gotOptIn = marketingOptIn
	return nil
}

func (m *welcomeMock) SetUserMarketingOptIn(userID string, optIn bool) error {
	m.toggleCalled = true
	m.gotToggle = optIn
	return nil
}

func newWelcomeService(existingOptIn bool) (*Service, *welcomeMock) {
	mock := &welcomeMock{mockPersistence: &mockPersistence{
		users: map[string]*api.User{
			"user-andy": {ID: "user-andy", Name: "auto-generate", MarketingOptIn: existingOptIn},
		},
	}}
	return &Service{persistence: mock}, mock
}

func welcomeRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Next()
	})
	r.POST("/user/welcome-complete", svc.completeWelcome)
	return r
}

func postWelcome(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/user/welcome-complete", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	welcomeRouter(svc).ServeHTTP(rec, req)
	return rec
}

// A ticked box is an affirmative consent and must be recorded as one.
func TestCompleteWelcome_GrantIsRecorded(t *testing.T) {
	RegisterTestingT(t)
	svc, mock := newWelcomeService(false)

	rec := postWelcome(t, svc, `{"name":"Ada Whitmore","marketing_opt_in":true}`)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.welcomeCalled).To(BeTrue())
	Expect(mock.gotName).To(Equal("Ada Whitmore"))
	Expect(mock.gotOptIn).To(Not(BeNil()))
	Expect(*mock.gotOptIn).To(BeTrue())
}

// An unticked box is a refusal, not an absence. It must reach persistence as an
// explicit false so it is timestamped and attributed, rather than being
// indistinguishable from a user who was never asked.
func TestCompleteWelcome_RefusalIsRecordedExplicitly(t *testing.T) {
	RegisterTestingT(t)
	svc, mock := newWelcomeService(false)

	rec := postWelcome(t, svc, `{"name":"Ada Whitmore","marketing_opt_in":false}`)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.gotOptIn).To(Not(BeNil()))
	Expect(*mock.gotOptIn).To(BeFalse())
}

// When the user already answered at sign-up the modal omits the question. The
// omission must leave the existing decision — and its evidence — untouched,
// rather than restamping it with this surface and this moment.
func TestCompleteWelcome_OmittedAnswerLeavesConsentAlone(t *testing.T) {
	RegisterTestingT(t)
	svc, mock := newWelcomeService(true)

	rec := postWelcome(t, svc, `{"name":"Ada Whitmore"}`)

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.welcomeCalled).To(BeTrue())
	Expect(mock.gotOptIn).To(BeNil())
	// The response still reports the user's real state, not a default false.
	Expect(rec.Body.String()).To(ContainSubstring(`"marketing_opt_in":true`))
}

// --- seeding from Sentinel at first provisioning -------------------------

func seedService(t *testing.T, handler http.HandlerFunc) (*Service, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cfg := &config.Config{}
	cfg.Security.IdentityService = srv.URL
	return &Service{config: cfg, identity: identity.NewConnector(cfg)}, srv.Close
}

func seedContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("jwt", "token-abc")
	return c
}

func TestSeedFromIdentity_CopiesSignupDecision(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		Expect(r.Header.Get("Authorization")).To(Equal("Bearer token-abc"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"user-andy",
			"username":"ada@flomation.co",
			"marketing_opt_in":true,
			"marketing_consent_at":"2026-08-22T10:20:39Z",
			"marketing_consent_source":"registration_form",
			"marketing_consent_version":"signup-v1"
		}`))
	})
	defer done()

	u := &api.User{ID: "user-andy"}
	svc.seedFromIdentity(seedContext(), u)

	Expect(u.MarketingOptIn).To(BeTrue())
	Expect(u.MarketingConsentAt).To(Not(BeNil()))
	Expect(u.MarketingConsentAt.UTC()).To(Equal(time.Date(2026, 8, 22, 10, 20, 39, 0, time.UTC)))
	Expect(u.MarketingConsentSource).To(Not(BeNil()))
	Expect(*u.MarketingConsentSource).To(Equal("registration_form"))
	Expect(*u.MarketingConsentVersion).To(Equal("signup-v1"))
}

// Sentinel holds the only copy of the address, and the marketing sync cannot
// subscribe anyone without it — so provisioning must carry it across.
func TestSeedFromIdentity_CopiesEmailAddress(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"user-andy","username":"ada@flomation.co"}`))
	})
	defer done()

	u := &api.User{ID: "user-andy"}
	svc.seedFromIdentity(seedContext(), u)

	Expect(u.EmailAddress).To(Not(BeNil()))
	Expect(*u.EmailAddress).To(Equal("ada@flomation.co"))
	// No decision was recorded at sign-up, so the user stays unasked.
	Expect(u.MarketingConsentAt).To(BeNil())
}

// A refusal at sign-up must carry across too, otherwise the product would ask
// again and the sign-up refusal would go unrecorded.
func TestSeedFromIdentity_CopiesSignupRefusal(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"user-andy",
			"marketing_opt_in":false,
			"marketing_consent_at":"2026-08-22T10:20:39Z",
			"marketing_consent_source":"registration_form"
		}`))
	})
	defer done()

	u := &api.User{ID: "user-andy"}
	svc.seedFromIdentity(seedContext(), u)

	Expect(u.MarketingOptIn).To(BeFalse())
	Expect(u.MarketingConsentAt).To(Not(BeNil()))
}

// SSO sign-ups and pre-existing accounts have no decision to copy. They must be
// left unasked so the product still puts the question to them.
func TestSeedFromIdentity_NoDecisionLeavesUserUnasked(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"user-andy","marketing_opt_in":false}`))
	})
	defer done()

	u := &api.User{ID: "user-andy"}
	svc.seedFromIdentity(seedContext(), u)

	Expect(u.MarketingOptIn).To(BeFalse())
	Expect(u.MarketingConsentAt).To(BeNil())
	Expect(u.MarketingConsentSource).To(BeNil())
}

// Provisioning must not depend on Sentinel being reachable.
func TestSeedFromIdentity_IdentityFailureIsNonFatal(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer done()

	u := &api.User{ID: "user-andy"}
	Expect(func() { svc.seedFromIdentity(seedContext(), u) }).To(Not(Panic()))
	Expect(u.MarketingConsentAt).To(BeNil())
}

// No JWT in context means nothing to authenticate the lookup with.
func TestSeedFromIdentity_NoTokenIsNonFatal(t *testing.T) {
	RegisterTestingT(t)

	svc, done := seedService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity service must not be called without a token")
	})
	defer done()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	u := &api.User{ID: "user-andy"}
	svc.seedFromIdentity(c, u)
	Expect(u.MarketingConsentAt).To(BeNil())
}
