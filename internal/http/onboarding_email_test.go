package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupUnsubscribeRouter(svc *Service) *gin.Engine {
	router := gin.New()
	// No auth middleware — matching the real registration, which is the
	// point of these tests.
	g := router.Group("/api/v1/onboarding/email")
	g.GET("/unsubscribe/:token", svc.getOnboardingEmailUnsubscribe)
	g.POST("/unsubscribe/:token", svc.postOnboardingEmailUnsubscribe)
	return router
}

func doUnsub(svc *Service, method, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	setupUnsubscribeRouter(svc).ServeHTTP(w,
		httptest.NewRequest(method, "/api/v1/onboarding/email/unsubscribe/"+token, nil))
	return w
}

// Mail clients and security appliances follow links to scan them. A GET that
// unsubscribed would silently opt people out of email they never chose to
// stop — and nobody would ever see it happen.
func TestUnsubscribeGetDoesNotActuallyUnsubscribe(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.optOutTokens = map[string]bool{"tok": true}

	w := doUnsub(setupTestService(mock), http.MethodGet, "tok")

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.optOutCalls).To(BeEmpty(), "GET must not change anything")
	Expect(w.Body.String()).To(ContainSubstring("<form method=\"POST\">"))
}

func TestUnsubscribePostOptsOut(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.optOutTokens = map[string]bool{"tok": true}

	w := doUnsub(setupTestService(mock), http.MethodPost, "tok")

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.optOutCalls).To(Equal([]string{"tok"}))
	Expect(w.Body.String()).To(ContainSubstring("not get any more onboarding emails"))
	Expect(w.Body.String()).ToNot(ContainSubstring("<form"))
}

// A stale or invented token gets the same page as a real one. Anything else
// turns the endpoint into a way to test whether a token exists.
func TestUnsubscribeAnswersTheSameForAnUnknownToken(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	known := newMockPersistence()
	known.optOutTokens = map[string]bool{"tok": true}
	real := doUnsub(setupTestService(known), http.MethodPost, "tok")

	unknown := newMockPersistence()
	fake := doUnsub(setupTestService(unknown), http.MethodPost, "not-a-real-token")

	Expect(fake.Code).To(Equal(real.Code))
	Expect(fake.Body.String()).To(Equal(real.Body.String()))
}

func TestUnsubscribeReportsAFailureHonestly(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.optOutErr = errors.New("database is down")

	w := doUnsub(setupTestService(mock), http.MethodPost, "tok")

	// Saying "done" when nothing was recorded would mean the next email
	// arrives after we told them it would not.
	Expect(w.Code).To(Equal(http.StatusInternalServerError))
	Expect(w.Body.String()).ToNot(ContainSubstring("not get any more onboarding emails"))
	Expect(w.Body.String()).To(ContainSubstring("try again"))
}

// The token comes straight from a URL, so it reaches the template from
// outside and must not be able to write markup into the page.
func TestUnsubscribePageEscapesWhateverIsInTheURL(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	w := doUnsub(setupTestService(mock), http.MethodGet, "%3Cscript%3Ealert(1)%3C/script%3E")

	Expect(w.Body.String()).ToNot(ContainSubstring("<script>alert(1)</script>"))
}

func TestUnsubscribePageIsNotIndexed(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	w := doUnsub(setupTestService(newMockPersistence()), http.MethodGet, "tok")
	Expect(w.Body.String()).To(ContainSubstring(`name="robots" content="noindex"`))
	Expect(w.Header().Get("Content-Type")).To(HavePrefix("text/html"))
}
