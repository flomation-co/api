package http

// Tests for the Jenkins jobs-options proxy. The invariants:
//
//   1. The upstream is per-request: the node's base_url arrives as a query
//      parameter, is turned into {base}/api/json?tree=jobs[name,url,color], and
//      the {"jobs":[...]} response is slimmed to sorted {"options":[{name,value}]}.
//   2. Missing/invalid base_url, blank username, and unreachable servers follow
//      the option-proxy convention: HTTP 200 + {"error": ...}, so the editor
//      shows the message and falls back to manual entry.
//   3. username + api_token are sent as HTTP Basic auth; a ${secrets.X}
//      reference without an environment is rejected before any fetch, and a
//      managed-credential reference is rejected with a clear message.
//
// The handler only touches persistence when resolving secret references, so no
// mockPersistence is wired here.

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupJenkinsJobsRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/jenkins-jobs", svc.getJenkinsJobs)
	return r
}

func getJenkinsJobOptions(r *gin.Engine, params map[string]string) map[string]interface{} {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/jenkins-jobs?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body
}

func TestJenkinsJobsURL(t *testing.T) {
	g := NewWithT(t)

	for input, want := range map[string]string{
		"http://ci:8080":        "http://ci:8080/api/json?tree=jobs%5Bname%2Curl%2Ccolor%5D",
		"http://ci:8080/":       "http://ci:8080/api/json?tree=jobs%5Bname%2Curl%2Ccolor%5D",
		"https://host/jenkins/": "https://host/jenkins/api/json?tree=jobs%5Bname%2Curl%2Ccolor%5D",
		// A trailing "?" (or any query/fragment) must not displace the forced
		// /api/json path into the query string.
		"http://ci:8080/x?a=1#f": "http://ci:8080/x/api/json?tree=jobs%5Bname%2Curl%2Ccolor%5D",
	} {
		got, err := jenkinsJobsURL(input)
		g.Expect(err).To(BeNil(), "input: %q", input)
		g.Expect(got).To(Equal(want), "input: %q", input)
	}

	for _, input := range []string{"", "ftp://host", "${parent.url}"} {
		_, err := jenkinsJobsURL(input)
		g.Expect(err).To(HaveOccurred(), "input: %q", input)
	}
}

func TestGetJenkinsJobs_SlimsSortsAndAuths(t *testing.T) {
	g := NewWithT(t)

	var gotPath, gotTree, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTree = r.URL.Query().Get("tree")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[
			{"name":"zeta"},
			{"name":"alpha"},
			{"name":""}
		]}`))
	}))
	defer upstream.Close()

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{
		"base_url":  upstream.URL,
		"username":  "admin",
		"api_token": "tok",
	})

	g.Expect(gotPath).To(Equal("/api/json"))
	g.Expect(gotTree).To(Equal("jobs[name,url,color]"))
	g.Expect(gotAuth).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("admin:tok"))))
	g.Expect(body).To(Not(HaveKey("error")))
	options := body["options"].([]interface{})
	g.Expect(options).To(HaveLen(2)) // blank name dropped
	g.Expect(options[0].(map[string]interface{})["name"]).To(Equal("alpha"))
	g.Expect(options[1].(map[string]interface{})["value"]).To(Equal("zeta"))
}

func TestIsCloudMetadataIP(t *testing.T) {
	g := NewWithT(t)
	// Non-link-local metadata endpoints the dialer must still refuse.
	g.Expect(isCloudMetadataIP(net.ParseIP("fd00:ec2::254"))).To(BeTrue())   // AWS IMDS over IPv6
	g.Expect(isCloudMetadataIP(net.ParseIP("100.100.100.200"))).To(BeTrue()) // Alibaba Cloud
	// Legitimate self-hosted targets must NOT be blocked here.
	g.Expect(isCloudMetadataIP(net.ParseIP("192.168.1.169"))).To(BeFalse())     // private LAN
	g.Expect(isCloudMetadataIP(net.ParseIP("fd12:3456:789a::1"))).To(BeFalse()) // other ULA
	g.Expect(isCloudMetadataIP(net.ParseIP("10.0.0.5"))).To(BeFalse())
}

func TestGetJenkinsJobs_MissingBaseURL(t *testing.T) {
	g := NewWithT(t)
	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{"username": "admin", "api_token": "tok"})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Jenkins URL"))
}

func TestGetJenkinsJobs_MissingUsername(t *testing.T) {
	g := NewWithT(t)
	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{"base_url": "http://ci:8080", "api_token": "tok"})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Username"))
}

func TestGetJenkinsJobs_UnreachableServer(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstream.Close() // immediately closed → connection refused

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{"base_url": upstream.URL, "username": "admin", "api_token": "tok"})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Could not reach"))
}

func TestGetJenkinsJobs_UnauthorisedUpstream(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{"base_url": upstream.URL, "username": "admin", "api_token": "bad"})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("unauthorised"))
}

func TestGetJenkinsJobs_CredentialRefRejectedClearly(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not contact the upstream for a managed-credential reference")
	}))
	defer upstream.Close()

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{
		"base_url":  upstream.URL,
		"username":  "admin",
		"api_token": "${credentials.MY_JENKINS}",
	})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Managed credentials"))
}

func TestGetJenkinsJobs_SecretRefWithoutEnvironment(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not contact the upstream when the secret cannot be resolved")
	}))
	defer upstream.Close()

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{
		"base_url":  upstream.URL,
		"username":  "admin",
		"api_token": "${secrets.JENKINS_TOKEN}",
	})
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("environment"))
}

// TestIntegrationLiveJenkinsJobs exercises the resolver against a real Jenkins
// server, skipped unless JENKINS_URL is set — mirroring the executor's live-test
// convention. Set JENKINS_URL, JENKINS_USER and JENKINS_TOKEN.
func TestIntegrationLiveJenkinsJobs(t *testing.T) {
	base := os.Getenv("JENKINS_URL")
	if base == "" {
		t.Skip("JENKINS_URL not set; skipping live Jenkins integration test")
	}
	g := NewWithT(t)

	r := setupJenkinsJobsRouter(&Service{})
	body := getJenkinsJobOptions(r, map[string]string{
		"base_url":  base,
		"username":  os.Getenv("JENKINS_USER"),
		"api_token": os.Getenv("JENKINS_TOKEN"),
	})
	g.Expect(body).To(Not(HaveKey("error")), "body: %v", body)
	g.Expect(body).To(HaveKey("options"))
	t.Logf("live jobs: %v", body["options"])
}
