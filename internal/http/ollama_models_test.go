package http

// Tests for the Ollama model-options proxy. The invariants:
//
//   1. The upstream is per-request: the node's endpoint arrives as a query
//      parameter and {"models": [...]} is slimmed to sorted
//      {"options": [{name, value}]}.
//   2. Missing/invalid endpoints and unreachable servers follow the
//      option-proxy convention: HTTP 200 + {"error": ...}, so the editor
//      shows the message and falls back to static options.
//   3. An empty model list is a *helpful message*, not options — a fresh
//      Ollama install genuinely has none.
//   4. A plain api_key is forwarded as a Bearer header; a ${secrets.X}
//      reference without an environment is rejected before any fetch.
//
// The handler only touches persistence when resolving secret references,
// so no mockPersistence is wired here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupOllamaModelsRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/ollama-models", svc.getOllamaModels)
	return r
}

func getOllamaModelOptions(r *gin.Engine, params map[string]string) map[string]interface{} {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/ollama-models?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body
}

func TestTagsURLNormalisation(t *testing.T) {
	g := NewWithT(t)

	for input, want := range map[string]string{
		"http://localhost:11434":          "http://localhost:11434/api/tags",
		"http://localhost:11434/":         "http://localhost:11434/api/tags",
		"http://localhost:11434/api":      "http://localhost:11434/api/tags",
		"http://localhost:11434/api/tags": "http://localhost:11434/api/tags",
		"http://localhost:11434/api/chat": "http://localhost:11434/api/tags",
		// A trailing "?" (or any query/fragment) must not displace the
		// forced /api/tags path into the query string — that would turn
		// the proxy into an arbitrary-path GET.
		"http://internal:8080/admin?":     "http://internal:8080/admin/api/tags",
		"http://internal:8080/x?a=1#frag": "http://internal:8080/x/api/tags",
	} {
		got, err := tagsURL(input)
		g.Expect(err).To(BeNil(), "input: %q", input)
		g.Expect(got).To(Equal(want), "input: %q", input)
	}

	for _, input := range []string{"${parent.url}", "localhost:11434", "ftp://host", ""} {
		_, err := tagsURL(input)
		g.Expect(err).To(HaveOccurred(), "input: %q", input)
	}
}

func TestGetOllamaModels_SlimsAndSorts(t *testing.T) {
	g := NewWithT(t)

	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"zephyr:latest","size":123},
			{"name":"llama3.2:1b","details":{"family":"llama"}},
			{"name":""}
		]}`))
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{"endpoint": upstream.URL})

	g.Expect(gotPath).To(Equal("/api/tags"))
	// No api_key param — no Authorization header on the upstream call.
	g.Expect(gotAuth).To(Equal(""))
	g.Expect(body).To(Not(HaveKey("error")))
	options := body["options"].([]interface{})
	g.Expect(options).To(HaveLen(2))
	first := options[0].(map[string]interface{})
	g.Expect(first["name"]).To(Equal("llama3.2:1b"))
	g.Expect(first["value"]).To(Equal("llama3.2:1b"))
	g.Expect(options[1].(map[string]interface{})["value"]).To(Equal("zephyr:latest"))
}

func TestGetOllamaModels_ForwardsBearerKey(t *testing.T) {
	g := NewWithT(t)

	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2"}]}`))
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{
		"endpoint": upstream.URL,
		"api_key":  "proxy-token",
	})

	g.Expect(gotAuth).To(Equal("Bearer proxy-token"))
	g.Expect(body).To(HaveKey("options"))
}

func TestGetOllamaModels_MissingEndpoint(t *testing.T) {
	g := NewWithT(t)

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, nil)
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Ollama Server URL"))
}

func TestGetOllamaModels_InvalidEndpoint(t *testing.T) {
	g := NewWithT(t)

	r := setupOllamaModelsRouter(&Service{})
	// Unresolved variable references and bare hostnames both land here.
	for _, endpoint := range []string{"${parent.url}", "localhost:11434", "ftp://host"} {
		body := getOllamaModelOptions(r, map[string]string{"endpoint": endpoint})
		g.Expect(body).To(HaveKey("error"), "endpoint: %q", endpoint)
	}
}

func TestGetOllamaModels_UnreachableServer(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstream.Close() // immediately closed → connection refused

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{"endpoint": upstream.URL})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Could not reach"))
}

func TestGetOllamaModels_EmptyServerIsHelpfulMessage(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{"endpoint": upstream.URL})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("no models installed"))
}

func TestGetOllamaModels_UnauthorisedUpstream(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{"endpoint": upstream.URL})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("check the API key"))
}

func TestGetOllamaModels_CredentialRefRejectedClearly(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not contact the upstream for a managed-credential reference")
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{
		"endpoint": upstream.URL,
		"api_key":  "${credentials.MY_OLLAMA_PROXY}",
	})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Managed credentials"))
}

func TestGetOllamaModels_SecretRefWithoutEnvironment(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not contact the upstream when the secret cannot be resolved")
	}))
	defer upstream.Close()

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{
		"endpoint": upstream.URL,
		"api_key":  "${secrets.OLLAMA_KEY}",
	})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("environment"))
}

// TestIntegrationLiveOllamaTags exercises the resolver against a real
// Ollama server, skipped unless OLLAMA_ENDPOINT is set — mirroring the
// executor's live-test convention.
func TestIntegrationLiveOllamaTags(t *testing.T) {
	endpoint := os.Getenv("OLLAMA_ENDPOINT")
	if endpoint == "" {
		t.Skip("OLLAMA_ENDPOINT not set; skipping live Ollama integration test")
	}
	g := NewWithT(t)

	r := setupOllamaModelsRouter(&Service{})
	body := getOllamaModelOptions(r, map[string]string{"endpoint": endpoint})
	g.Expect(body).To(HaveKey("options"), "body: %v", body)
	options := body["options"].([]interface{})
	g.Expect(len(options)).To(BeNumerically(">=", 1))
	t.Logf("live models: %v", options)
}
