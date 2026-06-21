package http

import (
	"testing"

	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/mtls"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// resolveRoutePath is the route that was registered twice (FLO-291): once in the
// trigger group and once on internalRouter.
const resolveRoutePath = "/api/v1/trigger/:id/resolve"

// countRoutes returns how many registered routes on the engine match the given
// method and path.
func countRoutes(routes gin.RoutesInfo, method, path string) int {
	n := 0
	for _, r := range routes {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// TestRegisterRoutesNoMTLSDoesNotPanic is the regression test for FLO-291.
//
// With mTLS disabled, internalRouter aliases the v1 group, so registering
// POST /trigger/:id/resolve on internalRouter collided with the same route in
// the trigger group and gin panicked at startup. Packaged installs run with
// mTLS off, so the API crashed on boot; mTLS environments masked the bug
// because the second registration landed on a separate internal engine.
func TestRegisterRoutesNoMTLSDoesNotPanic(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	s := &Service{engine: gin.New()}

	g.Expect(func() {
		s.registerRoutes(&config.Config{})
	}).ToNot(Panic(), "registerRoutes must not panic when mTLS is disabled")

	// The resolve route must be present exactly once on the public engine.
	g.Expect(countRoutes(s.engine.Routes(), "POST", resolveRoutePath)).
		To(Equal(1), "trigger resolve route should be registered exactly once")
}

// TestRegisterRoutesMTLSDoesNotPanic verifies the mTLS-enabled path: the resolve
// route lands on the dedicated internal engine, not the public one, and
// registration still succeeds.
func TestRegisterRoutesMTLSDoesNotPanic(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	s := &Service{engine: gin.New()}

	g.Expect(func() {
		s.registerRoutes(&config.Config{TLS: &mtls.TLSConfig{Enabled: true}})
	}).ToNot(Panic(), "registerRoutes must not panic when mTLS is enabled")

	g.Expect(s.internalEngine).ToNot(BeNil(), "mTLS should create a separate internal engine")

	// The public engine must NOT expose the internal resolve route...
	g.Expect(countRoutes(s.engine.Routes(), "POST", resolveRoutePath)).
		To(Equal(0), "resolve route must not be on the public engine under mTLS")
	// ...it lives on the internal engine instead, registered exactly once.
	g.Expect(countRoutes(s.internalEngine.Routes(), "POST", resolveRoutePath)).
		To(Equal(1), "resolve route should be on the internal engine under mTLS")
}
