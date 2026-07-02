package http

// Tests for the OpenRouter model-options proxy. The invariants:
//
//   1. Upstream {"data": [...]} is slimmed to {"options": [{name, value}]},
//      sorted by display name, ids with empty names fall back to the id.
//   2. The list is cached: a second request within the TTL does not hit
//      the upstream again.
//   3. Upstream failure with a warm (even expired) cache serves the stale
//      list rather than an error.
//   4. Upstream failure with a cold cache returns the option-proxy error
//      convention: HTTP 200 + {"error": ...}.
//
// The handler never touches persistence, so no mockPersistence is wired.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// setupOpenRouterModelsRouter wires the route with a synthetic auth
// middleware, mirroring the real per-route jwtMiddleware placement.
func setupOpenRouterModelsRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/openrouter-models", svc.getOpenRouterModels)
	return r
}

// withModelsUpstream points the package URL at a test server and resets
// the package cache, restoring both afterwards.
func withModelsUpstream(url string) func() {
	prevURL := openRouterModelsURL
	openRouterModelsURL = url
	openRouterModelsCache.mu.Lock()
	prevOptions, prevExpiry := openRouterModelsCache.options, openRouterModelsCache.expiresAt
	openRouterModelsCache.options = nil
	openRouterModelsCache.expiresAt = time.Time{}
	openRouterModelsCache.mu.Unlock()
	return func() {
		openRouterModelsURL = prevURL
		openRouterModelsCache.mu.Lock()
		openRouterModelsCache.options, openRouterModelsCache.expiresAt = prevOptions, prevExpiry
		openRouterModelsCache.mu.Unlock()
	}
}

func getModels(r *gin.Engine) map[string]interface{} {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/openrouter-models", nil)
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body
}

func TestGetOpenRouterModels_SlimsAndSorts(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"z/last-model","name":"Zeta Model","context_length":128000},
			{"id":"a/first-model","name":"Alpha Model","description":"big blob of text"},
			{"id":"n/no-name-model","name":""},
			{"id":"","name":"orphan entry, dropped"}
		]}`))
	}))
	defer upstream.Close()
	defer withModelsUpstream(upstream.URL)()

	r := setupOpenRouterModelsRouter(&Service{})
	body := getModels(r)

	g.Expect(body).To(HaveKey("options"))
	options := body["options"].([]interface{})
	g.Expect(options).To(HaveLen(3))
	first := options[0].(map[string]interface{})
	g.Expect(first["name"]).To(Equal("Alpha Model"))
	g.Expect(first["value"]).To(Equal("a/first-model"))
	// Empty display name falls back to the id; empty id is dropped.
	second := options[1].(map[string]interface{})
	g.Expect(second["name"]).To(Equal("n/no-name-model"))
	last := options[2].(map[string]interface{})
	g.Expect(last["value"]).To(Equal("z/last-model"))
}

func TestGetOpenRouterModels_CachesWithinTTL(t *testing.T) {
	g := NewWithT(t)

	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"a/m","name":"M"}]}`))
	}))
	defer upstream.Close()
	defer withModelsUpstream(upstream.URL)()

	r := setupOpenRouterModelsRouter(&Service{})
	g.Expect(getModels(r)).To(HaveKey("options"))
	g.Expect(getModels(r)).To(HaveKey("options"))
	g.Expect(hits).To(Equal(1), "second request within TTL must be served from cache")
}

func TestGetOpenRouterModels_ServesStaleOnUpstreamFailure(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	defer withModelsUpstream(upstream.URL)()

	// Warm the cache directly with an already-expired entry.
	openRouterModelsCache.mu.Lock()
	openRouterModelsCache.options = []api.InputOption{{Name: "Stale Model", Value: "stale/model"}}
	openRouterModelsCache.expiresAt = time.Now().Add(-time.Minute)
	openRouterModelsCache.mu.Unlock()

	r := setupOpenRouterModelsRouter(&Service{})
	body := getModels(r)
	g.Expect(body).To(HaveKey("options"))
	g.Expect(body).To(Not(HaveKey("error")))
	options := body["options"].([]interface{})
	g.Expect(options).To(HaveLen(1))
	g.Expect(options[0].(map[string]interface{})["value"]).To(Equal("stale/model"))
}

// TestGetOpenRouterModels_EmptyBodyIsFailure pins that a 200 response with
// no usable model entries (incident page, schema drift) is treated as a
// fetch failure: the stale cache keeps serving and an empty list is never
// cached, so the editor's static-options fallback can re-engage.
func TestGetOpenRouterModels_EmptyBodyIsFailure(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"catalogue temporarily unavailable"}}`))
	}))
	defer upstream.Close()
	defer withModelsUpstream(upstream.URL)()

	r := setupOpenRouterModelsRouter(&Service{})

	// Cold cache: the empty 200 must surface as an error, not as options.
	g.Expect(getModels(r)).To(HaveKey("error"))

	// Warm-but-expired cache: the stale list must survive the empty 200.
	openRouterModelsCache.mu.Lock()
	openRouterModelsCache.options = []api.InputOption{{Name: "Stale Model", Value: "stale/model"}}
	openRouterModelsCache.expiresAt = time.Now().Add(-time.Minute)
	openRouterModelsCache.mu.Unlock()

	body := getModels(r)
	g.Expect(body).To(Not(HaveKey("error")))
	options := body["options"].([]interface{})
	g.Expect(options).To(HaveLen(1))
	g.Expect(options[0].(map[string]interface{})["value"]).To(Equal("stale/model"))
}

func TestGetOpenRouterModels_ColdCacheUpstreamFailure(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	defer withModelsUpstream(upstream.URL)()

	r := setupOpenRouterModelsRouter(&Service{})
	body := getModels(r)
	g.Expect(body).To(HaveKey("error"))
}
