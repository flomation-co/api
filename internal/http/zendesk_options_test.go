package http

// Tests for the Zendesk group/organization dropdown proxies. The invariants:
//
//   1. Missing/invalid subdomain, email, or api_token follow the option-proxy
//      convention: HTTP 200 + {"error": ...}, so the editor shows the message
//      and falls back to manual entry — no network call is made.
//   2. A managed-credential api_token reference is rejected up front; a
//      ${secrets.X} reference without an environment is rejected before any
//      fetch.
//   3. The subdomain is validated to a bare handle (host-injection guard).
//   4. On success the named array of {id, name} is slimmed to sorted
//      {"options": [{name, value}]}.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupZendeskOptionsRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/zendesk-groups", svc.getZendeskGroups)
	r.GET("/api/v1/action/options/zendesk-organizations", svc.getZendeskOrganizations)
	return r
}

func getZendeskOptions(r *gin.Engine, endpoint string, params map[string]string) map[string]interface{} {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, endpoint+"?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body
}

func TestNormaliseZendeskSubdomainAPI(t *testing.T) {
	g := NewWithT(t)
	for input, want := range map[string]string{
		"acme":                     "acme",
		"acme.zendesk.com":         "acme",
		"https://acme.zendesk.com": "acme",
		"acme.evil.com/x":          "acme",
	} {
		g.Expect(normaliseZendeskSubdomainAPI(input)).To(Equal(want), "input: %q", input)
	}
}

func TestZendeskOptions_ValidationErrors(t *testing.T) {
	g := NewWithT(t)
	r := setupZendeskOptionsRouter(&Service{})
	ep := "/api/v1/action/options/zendesk-groups"

	// No subdomain.
	g.Expect(getZendeskOptions(r, ep, map[string]string{})["error"]).To(ContainSubstring("Subdomain"))
	// No email.
	g.Expect(getZendeskOptions(r, ep, map[string]string{"subdomain": "acme"})["error"]).To(ContainSubstring("Agent Email"))
	// No api_token.
	g.Expect(getZendeskOptions(r, ep, map[string]string{"subdomain": "acme", "email": "a@b.com"})["error"]).To(ContainSubstring("API Token"))
	// Managed credential rejected.
	g.Expect(getZendeskOptions(r, ep, map[string]string{"subdomain": "acme", "email": "a@b.com", "api_token": "${credentials.zd}"})["error"]).To(ContainSubstring("Managed credentials"))
	// Secret ref without environment.
	g.Expect(getZendeskOptions(r, ep, map[string]string{"subdomain": "acme", "email": "a@b.com", "api_token": "${secrets.zd}"})["error"]).To(ContainSubstring("environment"))
}

func TestZendeskOptions_HappyPath(t *testing.T) {
	g := NewWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.Header.Get("Authorization")).To(HavePrefix("Basic "))
		g.Expect(req.URL.Path).To(Equal("/api/v2/groups.json"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"groups":[{"id":2,"name":"Support"},{"id":1,"name":"Billing"}]}`))
	}))
	defer srv.Close()

	prev := zendeskOptionsHostOverride
	zendeskOptionsHostOverride = srv.URL
	defer func() { zendeskOptionsHostOverride = prev }()

	r := setupZendeskOptionsRouter(&Service{})
	body := getZendeskOptions(r, "/api/v1/action/options/zendesk-groups", map[string]string{
		"subdomain": "acme", "email": "a@b.com", "api_token": "plain-token",
	})
	opts, ok := body["options"].([]interface{})
	g.Expect(ok).To(BeTrue(), "body: %#v", body)
	g.Expect(opts).To(HaveLen(2))
	// Sorted by name (Billing before Support).
	first := opts[0].(map[string]interface{})
	g.Expect(first["name"]).To(Equal("Billing"))
	g.Expect(first["value"]).To(Equal("1"))
}
