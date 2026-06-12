package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// setupOnboardingRouter wires the onboarding endpoint with a fake
// account_id so the handler's getUserFromContext lookup finds a user
// in the mock store instead of returning 401.
func setupOnboardingRouter(svc *Service) *gin.Engine {
	router := gin.New()
	users := router.Group("/user")
	users.POST("/onboarding", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.updateOnboardingProgress)
	return router
}

// Reproduces the QA-reported bug: clicking "Skip" on the welcome step
// posts step=0, completed=true. Pre-fix the handler returned 400
// because Gin's `binding:"required"` rejected the zero value, so the
// optimistic local update never persisted and the popup re-appeared
// after any user refetch (org switch, profile save).
func Test_UpdateOnboarding_StepZeroCompleted_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(mock)
	router := setupOnboardingRouter(svc)

	body := strings.NewReader(`{"step": 0, "completed": true}`)
	req := httptest.NewRequest(http.MethodPost, "/user/onboarding", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"onboarding_step":0`))
	Expect(w.Body.String()).To(ContainSubstring(`"completed":true`))
}

// Step=0 with completed=false is also valid (the user advances past
// step 0 mid-tutorial — the persist sends the new step number, but
// any future flow that wants to checkpoint at 0 must work).
func Test_UpdateOnboarding_StepZeroNotCompleted_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(mock)
	router := setupOnboardingRouter(svc)

	body := strings.NewReader(`{"step": 0, "completed": false}`)
	req := httptest.NewRequest(http.MethodPost, "/user/onboarding", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
}

// Out-of-range values must still be rejected — the min/max bound on
// the binding tag replaces the previous manual range check.
func Test_UpdateOnboarding_StepOutOfRange_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(mock)
	router := setupOnboardingRouter(svc)

	for _, badStep := range []string{`8`, `-1`, `100`} {
		body := strings.NewReader(`{"step": ` + badStep + `, "completed": true}`)
		req := httptest.NewRequest(http.MethodPost, "/user/onboarding", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(http.StatusBadRequest), "step=%s should be rejected", badStep)
	}
}
