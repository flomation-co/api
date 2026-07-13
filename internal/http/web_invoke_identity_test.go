package http

import (
	"errors"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api/internal/config"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func TestForwardedUserID(t *testing.T) {
	RegisterTestingT(t)
	gin.SetMode(gin.TestMode)

	orig := resolveUserFromToken
	defer func() { resolveUserFromToken = orig }()
	resolveUserFromToken = func(_ string, token string) (string, error) {
		if token == "good" {
			return "user-1", nil
		}
		return "", errors.New("invalid token")
	}

	s := &Service{config: &config.Config{Security: config.SecurityConfig{IdentityService: "http://id"}}}
	ctx := func(auth string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req := httptest.NewRequest("POST", "/", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		c.Request = req
		return c
	}

	// A valid bearer resolves to the user.
	Expect(s.forwardedUserID(ctx("Bearer good"))).To(Equal("user-1"))
	// Scheme is case-insensitive.
	Expect(s.forwardedUserID(ctx("bearer good"))).To(Equal("user-1"))
	// Invalid token ⇒ anonymous (not rejected).
	Expect(s.forwardedUserID(ctx("Bearer bad"))).To(Equal(""))
	// No header / non-bearer ⇒ anonymous.
	Expect(s.forwardedUserID(ctx(""))).To(Equal(""))
	Expect(s.forwardedUserID(ctx("Basic zzz"))).To(Equal(""))
}
