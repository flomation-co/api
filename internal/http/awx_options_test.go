package http

// Tests for the Infrastructure ▸ AAP / AWX dropdown proxies. The invariants:
//
//  1. The upstream is per-request: the node's awx_url arrives as a query
//     parameter, the API root is discovered by sweeping /api/v2/ then
//     /api/controller/v2/, and the paginated {"results":[…]} envelope is slimmed
//     to sorted {"options":[{name,value}]} with the AWX id stringified as value.
//  2. EVERY failure is HTTP 200 + {"error": …}, never a 4xx/5xx, so the editor
//     renders the message inline and falls back to manual entry. The only non-200
//     path is checkPermission writing 401/403 itself.
//  3. A 404 sweeps to the next API root; a 401 does NOT — a merely-wrong token
//     must not be reported as "there is no AWX at this URL".
//  4. awx_url is caller-supplied, so the dialer refuses link-local and
//     cloud-metadata destinations and cross-host redirects are not followed, while
//     loopback/RFC1918 stay reachable (self-hosted AWX lives there).
//  5. api_token / awx_password arrive as ${secrets.X} references resolved
//     server-side; ${credentials.X} is rejected with a clear message.

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func setupAWXRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	for _, slug := range awxOptionRouteSlugs {
		r.GET("/api/v1/action/options/awx-"+slug, svc.awxOptions(slug))
	}
	r.GET("/api/v1/action/options/awx-adhoc-modules", svc.getAWXAdHocModules)
	return r
}

// getAWXOptions calls one picker and returns the decoded body plus the status, so
// a test can assert the "always 200" contract as well as the payload.
func getAWXOptions(r *gin.Engine, slug string, params map[string]string) (map[string]interface{}, int) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/awx-"+slug+"?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)

	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body, rec.Code
}

// optionNames / optionValues flatten the {"options":[…]} payload for assertions.
func optionNames(body map[string]interface{}) []string {
	var out []string
	for _, o := range body["options"].([]interface{}) {
		out = append(out, o.(map[string]interface{})["name"].(string))
	}
	return out
}

func optionValues(body map[string]interface{}) []string {
	var out []string
	for _, o := range body["options"].([]interface{}) {
		out = append(out, o.(map[string]interface{})["value"].(string))
	}
	return out
}

// awxStub is an AWX-shaped test server. It answers only under the given root, so
// pointing it at "/api/controller/v2/" reproduces the AAP gateway layout and
// exercises the sweep.
type awxStub struct {
	server   *httptest.Server
	root     string
	requests int32
	// paths records every path served, in order, so a test can prove which roots
	// were probed (and that a 401 did NOT sweep).
	paths []string
	auths []string
}

func newAWXStub(t *testing.T, root string, handler func(w http.ResponseWriter, r *http.Request)) *awxStub {
	t.Helper()
	stub := &awxStub{root: root}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.requests, 1)
		stub.paths = append(stub.paths, r.URL.Path)
		stub.auths = append(stub.auths, r.Header.Get("Authorization"))

		if !strings.HasPrefix(r.URL.Path, root) {
			// Not the root this instance serves — exactly what the other shape does.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// jobTemplatePage is the AWX list envelope for two job templates.
const jobTemplatePage = `{
	"count": 2,
	"next": null,
	"results": [
		{"id": 9, "name": "Zeta Deploy", "summary_fields": {"organization": {"id": 1, "name": "Default"}}},
		{"id": 7, "name": "Alpha Deploy", "summary_fields": {"organization": {"id": 1, "name": "Default"}}},
		{"id": 0, "name": "no id — dropped"},
		{"id": 8, "name": ""}
	]
}`

// ---------------------------------------------------------------------------
// URL / prefix normalisation
// ---------------------------------------------------------------------------

func TestAWXOptionsBaseURL(t *testing.T) {
	g := NewWithT(t)

	for input, want := range map[string]string{
		"http://192.168.80.27":         "http://192.168.80.27",
		"http://192.168.80.27/":        "http://192.168.80.27",
		"192.168.80.27":                "https://192.168.80.27", // bare host defaults to https
		"https://awx.example.com:8443": "https://awx.example.com:8443",
		// A pasted API URL is trimmed back to the base, or we'd build
		// /api/v2/api/v2/job_templates/.
		"https://awx.example.com/api":                "https://awx.example.com",
		"https://awx.example.com/api/v2":             "https://awx.example.com",
		"https://awx.example.com/api/v2/":            "https://awx.example.com",
		"https://aap.example.com/api/controller/v2/": "https://aap.example.com",
		// A context path is preserved (AWX behind a reverse proxy sub-path).
		"https://example.com/awx/": "https://example.com/awx",
		// Userinfo, query and fragment are stripped: a crafted base must not
		// smuggle credentials into the server-side request nor displace the API
		// path we append.
		"http://user:pw@192.168.80.27/x?a=1#f": "http://192.168.80.27/x",
	} {
		got, err := awxOptionsBaseURL(input)
		g.Expect(err).ToNot(HaveOccurred(), "input: %q", input)
		g.Expect(got).To(Equal(want), "input: %q", input)
	}

	for _, bad := range []string{"", "ftp://host", "${var.awx}", "${secrets.AWX_URL}"} {
		_, err := awxOptionsBaseURL(bad)
		g.Expect(err).To(HaveOccurred(), "input: %q", bad)
	}
}

func TestAWXNormalisePrefix(t *testing.T) {
	g := NewWithT(t)

	for input, want := range map[string]string{
		"":                    "",
		"api/v2":              "/api/v2/",
		"/api/v2":             "/api/v2/",
		"/api/v2/":            "/api/v2/",
		"/api/controller/v2":  "/api/controller/v2/",
		"${var.prefix}":       "", // unresolved → fall back to the sweep
		"/api/../../etc":      "", // traversal → ignored, fall back to the sweep
		"https://host/api/v2": "/api/v2/",
	} {
		g.Expect(awxNormalisePrefix(input)).To(Equal(want), "input: %q", input)
	}
}

func TestAWXRequestURLAlwaysHasTrailingSlash(t *testing.T) {
	g := NewWithT(t)

	// Django's APPEND_SLASH 301s a slash-less path, and Go turns a redirected POST
	// into a GET — the executor is bitten by that, so the path builder is pinned
	// here too.
	got, err := awxRequestURL("http://awx.local", "/api/v2/", "job_templates", url.Values{"page_size": {"200"}})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(got).To(Equal("http://awx.local/api/v2/job_templates/?page_size=200"))

	// A context-path base keeps its prefix.
	got, err = awxRequestURL("http://example.com/awx", "/api/controller/v2/", "hosts", url.Values{})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(got).To(Equal("http://example.com/awx/api/controller/v2/hosts/"))
}

// ---------------------------------------------------------------------------
// SSRF
// ---------------------------------------------------------------------------

func TestAWXOptionsDialControl(t *testing.T) {
	g := NewWithT(t)

	blocked := []string{
		"169.254.169.254:80", // AWS/GCP/Azure IMDS
		"[fe80::1]:80",       // link-local IPv6
		"[fd00:ec2::254]:80", // AWS IMDS over IPv6
		"100.100.100.200:80", // Alibaba Cloud
	}
	for _, addr := range blocked {
		g.Expect(awxOptionsDialControl("tcp", addr, nil)).To(HaveOccurred(), addr)
	}

	// Self-hosted AWX genuinely lives on these; blocking them would break the node.
	allowed := []string{"192.168.80.27:80", "10.0.0.5:443", "127.0.0.1:8043", "172.16.4.4:443"}
	for _, addr := range allowed {
		g.Expect(awxOptionsDialControl("tcp", addr, nil)).ToNot(HaveOccurred(), addr)
	}

	// An unresolved hostname passes through; the Control hook runs again on the
	// address it actually resolves to, which is what closes DNS rebinding.
	g.Expect(awxOptionsDialControl("tcp", net.JoinHostPort("awx.internal", "443"), nil)).ToNot(HaveOccurred())
}

func TestGetAWXOptions_CrossHostRedirectRefused(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/api/v2/job_templates/", http.StatusFound)
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   upstream.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Could not reach"))
}

func TestGetAWXOptions_UnreachableServer(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream.Close() // immediately closed → connection refused

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "inventories", map[string]string{
		"awx_url":   upstream.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("Could not reach AWX"))
}

// ---------------------------------------------------------------------------
// Happy path + API-root sweep
// ---------------------------------------------------------------------------

func TestGetAWXJobTemplates_SlimsSortsAndAuths(t *testing.T) {
	g := NewWithT(t)

	var gotQuery url.Values
	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jobTemplatePage))
	})

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))

	// The upstream AWX root answers first, so the gateway root is never probed.
	g.Expect(stub.paths).To(Equal([]string{"/api/v2/job_templates/"}))
	g.Expect(stub.auths[0]).To(Equal("Bearer tok"))
	g.Expect(gotQuery.Get("page_size")).To(Equal("200"))
	g.Expect(gotQuery.Get("order_by")).To(Equal("name"))

	// Sorted case-insensitively by label; the id-less and name-less rows dropped.
	g.Expect(optionNames(body)).To(Equal([]string{"Alpha Deploy (Default)", "Zeta Deploy (Default)"}))
	// AWX ids are JSON numbers — the option value must be the id as a STRING.
	g.Expect(optionValues(body)).To(Equal([]string{"7", "9"}))
}

// The AAP 2.5+ gateway moves the controller to /api/controller/v2/. A 404 on the
// upstream root must sweep to it.
func TestGetAWXOptions_SweepsToGatewayRootOn404(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/controller/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jobTemplatePage))
	})

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.paths).To(Equal([]string{
		"/api/v2/job_templates/",            // 404 — not upstream AWX
		"/api/controller/v2/job_templates/", // 200 — the gateway
	}))
	g.Expect(optionNames(body)).To(HaveLen(2))
}

// A 401 must NOT sweep. If it did, a merely-wrong token would surface as "there is
// no AWX at this URL" and send the operator to debug the wrong thing entirely.
func TestGetAWXOptions_UnauthorisedDoesNotSweep(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "bad",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("rejected the credentials"))
	g.Expect(stub.paths).To(HaveLen(1), "a 401 is conclusive — the gateway root must not be probed")
}

// Neither root serving the collection is a genuine "wrong URL", and says so.
func TestGetAWXOptions_NoAPIAtEitherRoot(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/nowhere/", func(w http.ResponseWriter, r *http.Request) {})

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("Could not find the AWX / AAP API"))
	g.Expect(stub.paths).To(HaveLen(2), "both roots must be tried before giving up")
}

// A 403 means the token authenticates but cannot read the collection — a different
// fix from a bad token, so a different message.
func TestGetAWXOptions_ForbiddenNamesTheCollection(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "inventories", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "read-scoped",
	})

	g.Expect(body["error"]).To(ContainSubstring("not allowed to list inventories"))
}

// An api_prefix override replaces the sweep entirely — the escape hatch for a
// deployment that serves the API somewhere we don't know about.
func TestGetAWXOptions_APIPrefixOverrideSkipsTheSweep(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/custom/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jobTemplatePage))
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":    stub.server.URL,
		"api_token":  "tok",
		"api_prefix": "custom/v2",
	})

	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.paths).To(Equal([]string{"/custom/v2/job_templates/"}))
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

// An instance with more than 200 job templates must still fill its dropdown: the
// walk follows AWX's RELATIVE `next` resolved against the base.
func TestGetAWXOptions_FollowsPagination(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "2":
			_, _ = w.Write([]byte(`{"next": null, "results": [{"id": 3, "name": "Page Two"}]}`))
		default:
			// AWX emits `next` as a path relative to the instance root, never as an
			// absolute URL.
			_, _ = w.Write([]byte(`{"next": "/api/v2/job_templates/?page=2&page_size=200", "results": [{"id": 1, "name": "Page One"}]}`))
		}
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})

	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(optionNames(body)).To(Equal([]string{"Page One", "Page Two"}))
	g.Expect(stub.paths).To(HaveLen(2))
}

// The walk is bounded: a controller whose `next` never terminates must not spin.
func TestGetAWXOptions_PaginationIsBounded(t *testing.T) {
	g := NewWithT(t)

	var served int32
	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&served, 1)
		// Always another page — an infinite cursor.
		_, _ = fmt.Fprintf(w, `{"next": "/api/v2/hosts/?page=%d", "results": [{"id": %d, "name": "host-%d"}]}`, n+1, n, n)
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "hosts", map[string]string{
		"awx_url":      stub.server.URL,
		"api_token":    "tok",
		"inventory_id": "1",
	})

	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(optionNames(body)).To(HaveLen(maxAWXOptionPages))
	g.Expect(int(atomic.LoadInt32(&served))).To(Equal(maxAWXOptionPages))
}

// A `next` pointing at another host must not move the walk off the instance the
// operator named — scheme and host are pinned back to the base.
func TestAWXNextURLPinsHostToBase(t *testing.T) {
	g := NewWithT(t)

	got, err := awxNextURL("https://awx.local", "https://awx.local/api/v2/hosts/?page=1", "/api/v2/hosts/?page=2")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(got).To(Equal("https://awx.local/api/v2/hosts/?page=2"))

	// An absolute `next` at an unrelated host keeps only its path and query.
	got, err = awxNextURL("https://awx.local", "https://awx.local/api/v2/hosts/?page=1", "https://evil.example.com/steal/?page=2")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(got).To(Equal("https://awx.local/steal/?page=2"))

	// A purely relative `next` resolves against the page we just read.
	got, err = awxNextURL("https://awx.local", "https://awx.local/api/v2/hosts/?page=1", "?page=3")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(got).To(Equal("https://awx.local/api/v2/hosts/?page=3"))
}

// ---------------------------------------------------------------------------
// Credential resolution
// ---------------------------------------------------------------------------

func TestGetAWXOptions_MissingURL(t *testing.T) {
	g := NewWithT(t)

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{"api_token": "tok"})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("AWX / AAP URL"))
}

func TestGetAWXOptions_MissingToken(t *testing.T) {
	g := NewWithT(t)

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "job-templates", map[string]string{"awx_url": "http://awx.local"})

	g.Expect(body["error"]).To(ContainSubstring("API Token"))
}

func TestGetAWXOptions_CredentialRefRejectedClearly(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("must not contact the upstream for a managed-credential reference")
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   upstream.URL,
		"api_token": "${credentials.MY_AWX}",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("Managed credentials"))
	g.Expect(body["error"]).To(ContainSubstring("the flow itself still runs"))
}

func TestGetAWXOptions_SecretRefWithoutEnvironment(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("must not contact the upstream when the secret cannot be resolved")
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   upstream.URL,
		"api_token": "${secrets.AWX_TOKEN}",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("environment"))
}

// The ${secrets.X} token is resolved server-side and reaches AWX as a Bearer
// header — the plaintext never transits the browser.
func TestGetAWXOptions_ResolvesSecretRefServerSide(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jobTemplatePage))
	})

	const envID = "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	// A user with no organisations is in "personal mode", where checkPermission
	// grants every permission — so this exercises the resolution path, not RBAC.
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}
	mock.secrets[envID+"/AWX_TOKEN"] = &api.EnvironmentSecret{
		EnvironmentID: envID, Name: "AWX_TOKEN", Value: "resolved-secret-token",
	}

	r := setupAWXRouter(&Service{persistence: mock})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":     stub.server.URL,
		"api_token":   "${secrets.AWX_TOKEN}",
		"environment": envID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.auths[0]).To(Equal("Bearer resolved-secret-token"))
}

func TestGetAWXOptions_SecretRefNotFoundInEnvironment(t *testing.T) {
	g := NewWithT(t)

	const envID = "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}

	r := setupAWXRouter(&Service{persistence: mock})
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":     "http://awx.local",
		"api_token":   "${secrets.MISSING}",
		"environment": envID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("not found in environment"))
}

// Basic auth is the fallback method: username + password, the password resolved
// through the same secret path as the token.
func TestGetAWXOptions_BasicAuth(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jobTemplatePage))
	})

	const envID = "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}
	mock.secrets[envID+"/AWX_PASSWORD"] = &api.EnvironmentSecret{
		EnvironmentID: envID, Name: "AWX_PASSWORD", Value: "s3cret",
	}

	r := setupAWXRouter(&Service{persistence: mock})
	body, code := getAWXOptions(r, "inventories", map[string]string{
		"awx_url":      stub.server.URL,
		"auth_method":  "basic",
		"awx_username": "admin",
		"awx_password": "${secrets.AWX_PASSWORD}",
		"environment":  envID,
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.auths[0]).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))))
}

func TestGetAWXOptions_BasicAuthMissingUsername(t *testing.T) {
	g := NewWithT(t)

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "inventories", map[string]string{
		"awx_url":      "http://awx.local",
		"auth_method":  "basic",
		"awx_password": "pw",
	})

	g.Expect(body["error"]).To(ContainSubstring("Username"))
}

// ---------------------------------------------------------------------------
// Insecure TLS opt-in
// ---------------------------------------------------------------------------

// A self-signed controller is refused by default and reachable only when the node
// ticks Allow Insecure TLS. The two clients are separate values, so the secure
// default cannot be weakened by a request that opted in.
func TestGetAWXOptions_InsecureTLSIsOptIn(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(jobTemplatePage))
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})

	// Default: the self-signed certificate is rejected.
	body, code := getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":   upstream.URL,
		"api_token": "tok",
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("Could not reach AWX"))

	// Opted in: it loads.
	body, code = getAWXOptions(r, "job-templates", map[string]string{
		"awx_url":        upstream.URL,
		"api_token":      "tok",
		"allow_insecure": "true",
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "body: %v", body)
	g.Expect(optionNames(body)).To(HaveLen(2))

	// And the secure client is still secure afterwards.
	secureCfg := awxOptionsHTTPClient.Transport.(*http.Transport).TLSClientConfig
	g.Expect(secureCfg.InsecureSkipVerify).To(BeFalse(), "the shared secure client must never be mutated")
	g.Expect(secureCfg.MinVersion).To(Equal(uint16(tls.VersionTLS12)))
}

// ---------------------------------------------------------------------------
// Inventory-scoped pickers
// ---------------------------------------------------------------------------

func TestGetAWXOptions_InventoryScopedNeedsAnInventory(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("must not call AWX before the operator has chosen an inventory")
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})

	for slug, want := range map[string]string{
		"hosts":             "its hosts",
		"groups":            "its groups",
		"inventory-sources": "its inventory sources",
	} {
		body, code := getAWXOptions(r, slug, map[string]string{
			"awx_url":   upstream.URL,
			"api_token": "tok",
		})
		g.Expect(code).To(Equal(http.StatusOK), slug)
		g.Expect(body["error"]).To(ContainSubstring(want), slug)

		// An unresolved variable reference is treated as "not chosen yet", not sent.
		body, _ = getAWXOptions(r, slug, map[string]string{
			"awx_url":      upstream.URL,
			"api_token":    "tok",
			"inventory_id": "${var.inv}",
		})
		g.Expect(body["error"]).To(ContainSubstring(want), slug)
	}
}

// AWX filters by primary key; a name would be a 400 from the controller. Catching
// it here gives the operator an answer instead of a mystery.
func TestGetAWXOptions_InventoryScopedRequiresNumericID(t *testing.T) {
	g := NewWithT(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("must not send a non-numeric inventory to AWX")
	}))
	defer upstream.Close()

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "hosts", map[string]string{
		"awx_url":      upstream.URL,
		"api_token":    "tok",
		"inventory_id": "Demo Inventory",
	})

	g.Expect(body["error"]).To(ContainSubstring("numeric ID"))
}

func TestGetAWXOptions_InventoryScopedFiltersByInventory(t *testing.T) {
	g := NewWithT(t)

	var gotQuery url.Values
	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"next": null, "results": [{"id": 12, "name": "web-01"}]}`))
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "hosts", map[string]string{
		"awx_url":      stub.server.URL,
		"api_token":    "tok",
		"inventory_id": "3",
	})

	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.paths).To(Equal([]string{"/api/v2/hosts/"}))
	g.Expect(gotQuery.Get("inventory")).To(Equal("3"))
	g.Expect(optionValues(body)).To(Equal([]string{"12"}))
}

// ---------------------------------------------------------------------------
// Kind-filtered credential pickers + labels
// ---------------------------------------------------------------------------

// AWX 400s a non-machine credential on an ad-hoc command and a non-scm one on a
// project, so those pickers must send the kind filter — offering the full list
// there would guarantee the error.
func TestGetAWXOptions_CredentialPickersFilterByKind(t *testing.T) {
	g := NewWithT(t)

	for slug, wantKind := range map[string]string{
		"machine-credentials": "ssh",
		"scm-credentials":     "scm",
		"credentials":         "", // the unfiltered list
	} {
		var gotQuery url.Values
		stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"next": null, "results": [
				{"id": 4, "name": "Deploy Key", "summary_fields": {"credential_type": {"name": "Source Control", "kind": "scm"}}}
			]}`))
		})

		r := setupAWXRouter(&Service{})
		body, _ := getAWXOptions(r, slug, map[string]string{
			"awx_url":   stub.server.URL,
			"api_token": "tok",
		})

		g.Expect(body).ToNot(HaveKey("error"), slug)
		g.Expect(stub.paths).To(Equal([]string{"/api/v2/credentials/"}), slug)
		g.Expect(gotQuery.Get("credential_type__kind")).To(Equal(wantKind), slug)

		// The label carries the type, so an SSH key is distinguishable from a
		// cloud key at a glance.
		g.Expect(optionNames(body)).To(Equal([]string{"Deploy Key (Source Control)"}), slug)
	}
}

func TestAWXOptionLabels(t *testing.T) {
	g := NewWithT(t)

	var jt awxRow
	jt.ID, jt.Name = 1, "Demo Job Template"
	jt.Summary.Organization.Name = "Default"
	g.Expect(awxOptionLabel(jt, awxLabelOrg)).To(Equal("Demo Job Template (Default)"))
	g.Expect(awxOptionLabel(jt, awxLabelPlain)).To(Equal("Demo Job Template"))

	// No organization in summary_fields → the bare name, never "Name ()".
	var orphan awxRow
	orphan.ID, orphan.Name = 2, "Orphan"
	g.Expect(awxOptionLabel(orphan, awxLabelOrg)).To(Equal("Orphan"))

	// Credential type falls back to the raw kind on an older serializer.
	var cred awxRow
	cred.ID, cred.Name = 3, "Key"
	cred.Summary.CredentialType.Kind = "ssh"
	g.Expect(awxOptionLabel(cred, awxLabelCredentialType)).To(Equal("Key (ssh)"))

	var sched awxRow
	sched.ID, sched.Name, sched.NextRun = 4, "Nightly", "2026-07-15T02:00:00Z"
	sched.Summary.UnifiedJobTemplate.Name = "Demo Job Template"
	g.Expect(awxOptionLabel(sched, awxLabelSchedule)).
		To(Equal("Nightly — Demo Job Template (next run: 2026-07-15T02:00:00Z)"))

	// next_run is null on a disabled schedule — no dangling "(next run: )".
	var disabled awxRow
	disabled.ID, disabled.Name = 5, "Paused"
	g.Expect(awxOptionLabel(disabled, awxLabelSchedule)).To(Equal("Paused"))
}

// ---------------------------------------------------------------------------
// Ad-hoc modules (the bespoke picker)
// ---------------------------------------------------------------------------

// The allow-list is an admin-editable runtime setting, returned as a bare array —
// not a paginated collection. A hardcoded list would be wrong in both directions
// on a customised instance.
func TestGetAWXAdHocModules(t *testing.T) {
	g := NewWithT(t)

	stub := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"AD_HOC_COMMANDS": ["shell", "command", "ping", "setup", ""],
			"AWX_ISOLATION_BASE_PATH": "/tmp"
		}`))
	})

	r := setupAWXRouter(&Service{})
	body, code := getAWXOptions(r, "adhoc-modules", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})

	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"))
	g.Expect(stub.paths).To(Equal([]string{"/api/v2/settings/jobs/"}))
	// Sorted, blanks dropped, name == value (AWX stores short names).
	g.Expect(optionNames(body)).To(Equal([]string{"command", "ping", "setup", "shell"}))
	g.Expect(optionValues(body)).To(Equal([]string{"command", "ping", "setup", "shell"}))
}

func TestGetAWXAdHocModules_SweepsAndSoftFails(t *testing.T) {
	g := NewWithT(t)

	// The gateway shape: the ad-hoc picker sweeps too.
	stub := newAWXStub(t, "/api/controller/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"AD_HOC_COMMANDS": ["ping"]}`))
	})

	r := setupAWXRouter(&Service{})
	body, _ := getAWXOptions(r, "adhoc-modules", map[string]string{
		"awx_url":   stub.server.URL,
		"api_token": "tok",
	})
	g.Expect(optionNames(body)).To(Equal([]string{"ping"}))

	// A non-AWX server that answers 200 with HTML must soft-fail, not 500.
	html := newAWXStub(t, "/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>hello</body></html>`))
	})
	body, code := getAWXOptions(r, "adhoc-modules", map[string]string{
		"awx_url":   html.server.URL,
		"api_token": "tok",
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("did not answer like an AWX"))
}

// ---------------------------------------------------------------------------
// Registration guards
// ---------------------------------------------------------------------------

func TestAWXActionsResolveTheirSubCategory(t *testing.T) {
	g := NewWithT(t)

	cat := getCategoryForAction("infrastructure/awx/job_template_launch")
	g.Expect(cat).ToNot(BeNil())
	g.Expect(cat.Key).To(Equal("infrastructure"))
	g.Expect(cat.Name).To(Equal("Infrastructure"))
	g.Expect(cat.SubKey).To(Equal("infrastructure/awx"))
	g.Expect(cat.SubName).To(Equal("AAP / AWX"))
	// The icon must exist in the editor's iconPaths, or every AWX action renders
	// as a "?".
	g.Expect(cat.SubIcon).To(Equal("ansible"))
	g.Expect(cat.SubDescription).To(ContainSubstring("Ansible Automation Platform"))
}

func TestAWXDynamicOptionsAreRegistered(t *testing.T) {
	g := NewWithT(t)

	for marker, wantEndpoint := range map[string]string{
		"infrastructure/awx/job_template_launch#job_template_id":          "/api/v1/action/options/awx-job-templates",
		"infrastructure/awx/job_template_launch#inventory_id":             "/api/v1/action/options/awx-inventories",
		"infrastructure/awx/job_template_launch#credentials":              "/api/v1/action/options/awx-credentials",
		"infrastructure/awx/job_template_launch#labels":                   "/api/v1/action/options/awx-labels",
		"infrastructure/awx/job_template_launch#instance_groups":          "/api/v1/action/options/awx-instance-groups",
		"infrastructure/awx/job_template_launch#execution_environment_id": "/api/v1/action/options/awx-execution-environments",
		"infrastructure/awx/workflow_launch#workflow_template_id":         "/api/v1/action/options/awx-workflow-templates",
		"infrastructure/awx/adhoc_command_run#module_name":                "/api/v1/action/options/awx-adhoc-modules",
		"infrastructure/awx/adhoc_command_run#credential_id":              "/api/v1/action/options/awx-machine-credentials",
		"infrastructure/awx/project_create#credential_id":                 "/api/v1/action/options/awx-scm-credentials",
		"infrastructure/awx/project_create#organization_id":               "/api/v1/action/options/awx-organizations",
		"infrastructure/awx/host_get#host_id":                             "/api/v1/action/options/awx-hosts",
		"infrastructure/awx/group_get#group_id":                           "/api/v1/action/options/awx-groups",
		"infrastructure/awx/inventory_source_sync#inventory_source_id":    "/api/v1/action/options/awx-inventory-sources",
		"infrastructure/awx/credential_create#credential_type_id":         "/api/v1/action/options/awx-credential-types",
		"infrastructure/awx/schedule_delete#schedule_id":                  "/api/v1/action/options/awx-schedules",
		"infrastructure/awx/project_sync#project_id":                      "/api/v1/action/options/awx-projects",
		// The trigger's template pickers are markers too.
		"trigger/awx_webhook#job_template_id":      "/api/v1/action/options/awx-job-templates",
		"trigger/awx_webhook#workflow_template_id": "/api/v1/action/options/awx-workflow-templates",
	} {
		got, ok := dynamicOptionsMetadata[marker]
		g.Expect(ok).To(BeTrue(), "missing dynamic-options marker: %s", marker)
		g.Expect(got.Endpoint).To(Equal(wantEndpoint), marker)

		// Every AWX picker needs the whole connection: the upstream is the node's
		// own controller, so without these the proxy has nothing to call.
		for _, p := range []string{"awx_url", "auth_method", "api_token", "awx_username", "awx_password", "allow_insecure", "api_prefix", "environment"} {
			g.Expect(got.Params).To(ContainElement(p), marker)
		}
	}

	// Inventory-scoped pickers must forward the chosen inventory, or they would
	// list every host on the controller instead of the ones in it.
	for _, marker := range []string{
		"infrastructure/awx/host_get#host_id",
		"infrastructure/awx/group_get#group_id",
		"infrastructure/awx/inventory_source_sync#inventory_source_id",
	} {
		g.Expect(dynamicOptionsMetadata[marker].Params).To(ContainElement("inventory_id"), marker)
	}

	// The unscoped ones must NOT — an inventory picker scoped to itself is circular.
	g.Expect(dynamicOptionsMetadata["infrastructure/awx/job_template_launch#inventory_id"].Params).
		ToNot(ContainElement("inventory_id"))
}

// Every awx-* endpoint a marker points at must be a route the service actually
// serves. A mismatch is otherwise a silent 404: the editor shows no error at all
// and just falls back to manual entry, which is the hardest kind of bug to notice.
func TestAWXOptionEndpointsHaveRoutes(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	s := &Service{engine: gin.New()}
	s.registerRoutes(&config.Config{})

	served := map[string]bool{}
	for _, route := range s.engine.Routes() {
		if route.Method == http.MethodGet {
			served[route.Path] = true
		}
	}

	markers := 0
	for marker, opts := range dynamicOptionsMetadata {
		if !strings.HasPrefix(opts.Endpoint, "/api/v1/action/options/awx-") {
			continue
		}
		markers++
		g.Expect(served[opts.Endpoint]).To(BeTrue(),
			"marker %s points at %s, which the service does not serve", marker, opts.Endpoint)
	}

	// Guard the guard: if the init() table ever stops registering, the loop above
	// would vacuously pass.
	g.Expect(markers).To(BeNumerically(">=", 70), "the AWX dropdown markers are not registered")
}

// The route list and the resource table must not drift apart: a resource with no
// route is a dead dropdown, and a route with no resource panics on boot.
func TestAWXOptionRouteSlugsMatchResources(t *testing.T) {
	g := NewWithT(t)

	g.Expect(awxOptionRouteSlugs).To(HaveLen(len(awxOptionResources)))
	for _, slug := range awxOptionRouteSlugs {
		_, ok := awxOptionResources[slug]
		g.Expect(ok).To(BeTrue(), "route slug %q has no awxOptionResource — awxOptions() would panic on boot", slug)
	}

	// awx-adhoc-modules is deliberately NOT a resource: it reads a settings key,
	// not a collection, and has its own handler + route.
	_, ok := awxOptionResources["adhoc-modules"]
	g.Expect(ok).To(BeFalse())
}

// Every endpoint named in the marker table must be either a table-driven resource
// or the one bespoke handler — a typo'd endpoint name would otherwise register a
// marker pointing at a route that does not exist.
func TestAWXOptionMarkerEndpointsAreKnown(t *testing.T) {
	g := NewWithT(t)

	for endpoint := range awxOptionMarkers {
		slug := strings.TrimPrefix(endpoint, "awx-")
		if slug == "adhoc-modules" {
			continue
		}
		_, ok := awxOptionResources[slug]
		g.Expect(ok).To(BeTrue(), "marker table names %q, which has no awxOptionResource", endpoint)
	}
}

// ---------------------------------------------------------------------------
// Live integration (opt-in)
// ---------------------------------------------------------------------------

// TestIntegrationLiveAWXOptions exercises the proxies against a real controller,
// skipped unless AWX_URL is set — mirroring the executor's live-test convention.
// Set AWX_URL and AWX_TOKEN (and optionally AWX_INSECURE=true).
func TestIntegrationLiveAWXOptions(t *testing.T) {
	base := os.Getenv("AWX_URL")
	if base == "" {
		t.Skip("AWX_URL not set; skipping live AWX integration test")
	}
	g := NewWithT(t)

	r := setupAWXRouter(&Service{})
	for _, slug := range []string{"job-templates", "inventories", "organizations", "adhoc-modules"} {
		body, code := getAWXOptions(r, slug, map[string]string{
			"awx_url":        base,
			"api_token":      os.Getenv("AWX_TOKEN"),
			"allow_insecure": os.Getenv("AWX_INSECURE"),
		})
		g.Expect(code).To(Equal(http.StatusOK), slug)
		g.Expect(body).ToNot(HaveKey("error"), "%s: %v", slug, body["error"])
		t.Logf("live %s: %v", slug, body["options"])
	}
}
