package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/connector/identity"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

var errTopUp = errors.New("write failed")

// topUpMock records whether the read path wrote the address back.
type topUpMock struct {
	*mockPersistence

	topUpCalled bool
	gotUserID   string
	gotEmail    string
	topUpErr    error
}

func (m *topUpMock) SetUserEmailAddressIfMissing(userID, email string) (int64, error) {
	m.topUpCalled = true
	m.gotUserID = userID
	m.gotEmail = email
	if m.topUpErr != nil {
		return 0, m.topUpErr
	}
	return 1, nil
}

// newTopUpService wires getUser against a stub identity service. stored is the
// address already held in our own users row, nil meaning none.
func newTopUpService(t *testing.T, stored *string, identityBody string) (*gin.Engine, *topUpMock, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identityBody == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(identityBody))
	}))

	cfg := &config.Config{}
	cfg.Security.IdentityService = srv.URL

	mock := &topUpMock{mockPersistence: &mockPersistence{
		users: map[string]*api.User{
			"user-andy": {ID: "user-andy", Name: "Ada Whitmore", EmailAddress: stored},
		},
	}}

	svc := &Service{persistence: mock, config: cfg, identity: identity.NewConnector(cfg)}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-andy")
		c.Set("jwt", "token-abc")
		c.Next()
	})
	r.GET("/user", svc.getUser)
	return r, mock, srv.Close
}

func getUserBody(t *testing.T, r *gin.Engine) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/user", nil))
	Expect(rec.Code).To(Equal(http.StatusOK))
	var out map[string]any
	Expect(json.Unmarshal(rec.Body.Bytes(), &out)).To(Succeed())
	return out
}

// The address exists only in Sentinel until something writes it down. This read
// path is the one place that already holds it, so it is where the gap closes.
func TestGetUser_PersistsMissingEmailAddress(t *testing.T) {
	RegisterTestingT(t)

	r, mock, done := newTopUpService(t, nil, `{"id":"user-andy","username":"ada@flomation.co"}`)
	defer done()

	body := getUserBody(t, r)

	Expect(mock.topUpCalled).To(BeTrue())
	Expect(mock.gotUserID).To(Equal("user-andy"))
	Expect(mock.gotEmail).To(Equal("ada@flomation.co"))
	Expect(body["email_address"]).To(Equal("ada@flomation.co"))
}

// Profile loads are frequent. Once the address is stored, the read path must go
// back to being a read.
func TestGetUser_DoesNotWriteWhenEmailAlreadyStored(t *testing.T) {
	RegisterTestingT(t)

	stored := "ada@flomation.co"
	r, mock, done := newTopUpService(t, &stored, `{"id":"user-andy","username":"ada@flomation.co"}`)
	defer done()

	getUserBody(t, r)

	Expect(mock.topUpCalled).To(BeFalse())
}

// Sentinel is authoritative for the address, so the response still reflects it
// even though we deliberately do not overwrite our stored copy here.
func TestGetUser_ChangedEmailIsShownButNotOverwritten(t *testing.T) {
	RegisterTestingT(t)

	stored := "old@flomation.co"
	r, mock, done := newTopUpService(t, &stored, `{"id":"user-andy","username":"new@flomation.co"}`)
	defer done()

	body := getUserBody(t, r)

	Expect(body["email_address"]).To(Equal("new@flomation.co"))
	Expect(mock.topUpCalled).To(BeFalse())
}

// An account with no address at the identity service has nothing to store.
func TestGetUser_EmptyIdentityUsernameWritesNothing(t *testing.T) {
	RegisterTestingT(t)

	r, mock, done := newTopUpService(t, nil, `{"id":"user-andy","username":""}`)
	defer done()

	getUserBody(t, r)

	Expect(mock.topUpCalled).To(BeFalse())
}

// A failed write must not fail the request — the response is already correct.
func TestGetUser_TopUpFailureIsNonFatal(t *testing.T) {
	RegisterTestingT(t)

	r, mock, done := newTopUpService(t, nil, `{"id":"user-andy","username":"ada@flomation.co"}`)
	defer done()
	mock.topUpErr = errTopUp

	body := getUserBody(t, r)

	Expect(mock.topUpCalled).To(BeTrue())
	Expect(body["email_address"]).To(Equal("ada@flomation.co"))
}

// An unreachable identity service leaves the stored copy alone.
func TestGetUser_IdentityFailureWritesNothing(t *testing.T) {
	RegisterTestingT(t)

	r, mock, done := newTopUpService(t, nil, "")
	defer done()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/user", nil))

	Expect(rec.Code).To(Equal(http.StatusOK))
	Expect(mock.topUpCalled).To(BeFalse())
}
