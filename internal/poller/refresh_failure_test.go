package poller

// Tests for the consecutive-failures + revoke-on-permanent-error
// machinery introduced for both the Google account refresh poller
// and the generic credential refresh poller.
//
// The interesting behaviours to cover:
//
//   1. classifyRefreshError correctly identifies the three Google
//      OAuth markers as permanent and tolerates everything else.
//   2. A transient failure increments the counter and stays at
//      status='error' until threshold hits, then flips to 'revoked'.
//   3. A permanent failure flips straight to 'revoked' on the very
//      first attempt, no waiting.
//   4. The poller stops calling the OAuth endpoint for accounts that
//      have already been revoked — verified by checking the mock
//      records no further refresh attempts after the revoke.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
)

func TestClassifyRefreshError_InvalidGrant(t *testing.T) {
	RegisterTestingT(t)
	err := &refreshError{StatusCode: 400, Body: `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`}
	Expect(classifyRefreshError(err)).To(BeTrue(),
		"invalid_grant must be classified permanent — Google never lets us recover")
}

func TestClassifyRefreshError_UnauthorizedClient(t *testing.T) {
	RegisterTestingT(t)
	// The exact body from the user's WARN log line that triggered this work.
	err := &refreshError{StatusCode: 401, Body: `{"error":"unauthorized_client","error_description":"Unauthorized"}`}
	Expect(classifyRefreshError(err)).To(BeTrue())
}

func TestClassifyRefreshError_InvalidClient(t *testing.T) {
	RegisterTestingT(t)
	err := &refreshError{StatusCode: 401, Body: `{"error":"invalid_client"}`}
	Expect(classifyRefreshError(err)).To(BeTrue())
}

func TestClassifyRefreshError_TransientShapes(t *testing.T) {
	RegisterTestingT(t)

	cases := []*refreshError{
		{StatusCode: 500, Body: `internal server error`},
		{StatusCode: 503, Body: `Service unavailable`},
		{StatusCode: 429, Body: `{"error":"rate_limit_exceeded"}`},
		{StatusCode: 0, Body: `empty access_token in response`},
		nil,
	}
	for _, c := range cases {
		Expect(classifyRefreshError(c)).To(BeFalse(),
			"transient error %v must not be classified permanent", c)
	}

	// A plain non-refreshError must also classify as transient — we
	// must not panic and must not pretend it's permanent.
	Expect(classifyRefreshError(errors.New("network reset"))).To(BeFalse())
	Expect(classifyRefreshError(nil)).To(BeFalse())
}

// trackingGoogleMock records every persistence call the poller makes
// so a test can assert refreshAccount stopped being attempted once a
// row was marked revoked.
type trackingGoogleMock struct {
	mu sync.Mutex

	rows []persistence.GoogleAccountRefreshRow

	// Each tick we mutate accountStatus to whatever
	// RecordGoogleAccountRefreshFailure returns. The next call to
	// GetGoogleAccountsNeedingRefresh filters out 'revoked' rows so
	// the poller stops trying them.
	accountStatus map[string]string

	// Counter calls so tests can assert "this account was attempted N times".
	refreshFailureCalls []failureCall
}

type failureCall struct {
	ID        string
	LastError string
	Permanent bool
	Threshold int
}

func newTrackingGoogleMock(rows []persistence.GoogleAccountRefreshRow) *trackingGoogleMock {
	m := &trackingGoogleMock{
		rows:          rows,
		accountStatus: map[string]string{},
	}
	for _, r := range rows {
		m.accountStatus[r.ID] = "active"
	}
	return m
}

func (m *trackingGoogleMock) GetGoogleAccountsNeedingRefresh(_ time.Duration) ([]persistence.GoogleAccountRefreshRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []persistence.GoogleAccountRefreshRow
	for _, r := range m.rows {
		if m.accountStatus[r.ID] != "revoked" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *trackingGoogleMock) StoreGoogleAccountAccessToken(id, _ string, _ *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountStatus[id] = "active"
	return nil
}

func (m *trackingGoogleMock) UpdateGoogleAccountStatus(id, status string, _ *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountStatus[id] = status
	return nil
}

func (m *trackingGoogleMock) RecordGoogleAccountRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshFailureCalls = append(m.refreshFailureCalls, failureCall{id, lastError, permanent, threshold})
	// Count this call's contribution toward the threshold.
	count := 0
	for _, c := range m.refreshFailureCalls {
		if c.ID == id {
			count++
		}
	}
	newStatus := "error"
	if permanent || count >= threshold {
		newStatus = "revoked"
	}
	m.accountStatus[id] = newStatus
	return newStatus, nil
}

func (m *trackingGoogleMock) GetTriggerGoogleAccountsNeedingRefresh(_ time.Duration) ([]persistence.GoogleAccountRefreshRow, error) {
	return nil, nil
}
func (m *trackingGoogleMock) StoreTriggerGoogleAccountAccessToken(string, string, *time.Time) error {
	return nil
}
func (m *trackingGoogleMock) UpdateTriggerGoogleAccountStatus(string, string, *string) error {
	return nil
}
func (m *trackingGoogleMock) RecordTriggerGoogleAccountRefreshFailure(string, string, bool, int) (string, error) {
	return "error", nil
}

// failingGoogleOAuthServer returns a stub Google token endpoint that
// always 401s with the supplied body. The returned URL replaces
// googleTokenURL via the test poll path: we call refreshAccount
// directly so we can substitute the URL via a custom poller.
func failingGoogleOAuthServer(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestGooglePoller_PermanentErrorRevokesImmediately(t *testing.T) {
	RegisterTestingT(t)

	mock := newTrackingGoogleMock([]persistence.GoogleAccountRefreshRow{
		{ID: "acc-1", RefreshToken: "rt-1", Email: "ada@example.com", Purpose: "email_read"},
	})

	// Stub Google: returns the exact error body that triggered this work.
	srv := failingGoogleOAuthServer(`{"error":"unauthorized_client","error_description":"Unauthorized"}`, http.StatusUnauthorized)
	defer srv.Close()

	rp := &GoogleAccountRefreshPoller{
		persistence:  mock,
		clientID:     "id",
		clientSecret: "secret",
		client:       srv.Client(),
	}
	// Point the refresh call at our stub instead of Google.
	overrideGoogleTokenURL(srv.URL, func() {
		rp.poll()
	})

	Expect(mock.refreshFailureCalls).To(HaveLen(1), "first failure must record once")
	Expect(mock.refreshFailureCalls[0].Permanent).To(BeTrue(),
		"unauthorized_client must classify as permanent on the first attempt")
	Expect(mock.accountStatus["acc-1"]).To(Equal("revoked"),
		"row must transition to revoked immediately")

	// Second poll: the mock filters out revoked rows. No further
	// refresh attempts should fire.
	overrideGoogleTokenURL(srv.URL, func() {
		rp.poll()
	})
	Expect(mock.refreshFailureCalls).To(HaveLen(1),
		"revoked rows must not be retried — poller stops touching them")
}

func TestGooglePoller_TransientErrorRetriesUntilThreshold(t *testing.T) {
	RegisterTestingT(t)

	mock := newTrackingGoogleMock([]persistence.GoogleAccountRefreshRow{
		{ID: "acc-1", RefreshToken: "rt-1", Email: "ada@example.com", Purpose: "email_read"},
	})

	// Transient: Google is down with a 503. Refresh fails but should
	// not be classified permanent.
	srv := failingGoogleOAuthServer(`Service Unavailable`, http.StatusServiceUnavailable)
	defer srv.Close()

	rp := &GoogleAccountRefreshPoller{
		persistence:  mock,
		clientID:     "id",
		clientSecret: "secret",
		client:       srv.Client(),
	}

	// Run the poll MaxConsecutiveRefreshFailures times. The first
	// (threshold-1) calls leave the row at 'error'; the threshold-th
	// call flips it to 'revoked' and stops further attempts.
	for i := 0; i < MaxConsecutiveRefreshFailures; i++ {
		overrideGoogleTokenURL(srv.URL, func() {
			rp.poll()
		})
	}

	Expect(mock.refreshFailureCalls).To(HaveLen(MaxConsecutiveRefreshFailures),
		"every tick up to the threshold must attempt a refresh")
	for _, c := range mock.refreshFailureCalls {
		Expect(c.Permanent).To(BeFalse(),
			"503 is transient — must not be classified permanent")
	}
	Expect(mock.accountStatus["acc-1"]).To(Equal("revoked"),
		"after threshold consecutive failures the row must transition to revoked")

	// One more tick: revoked row is now filtered out.
	overrideGoogleTokenURL(srv.URL, func() {
		rp.poll()
	})
	Expect(mock.refreshFailureCalls).To(HaveLen(MaxConsecutiveRefreshFailures),
		"no further attempts after revoke")
}

// overrideGoogleTokenURL temporarily redirects refreshAccount's POST
// target to a stub server. Restores the original on return.
func overrideGoogleTokenURL(stubURL string, body func()) {
	orig := googleTokenURLOverride
	googleTokenURLOverride = stubURL
	defer func() { googleTokenURLOverride = orig }()
	body()
}
