package http

// Tests for the Azure option proxies.
//
// The invariants under test, in rough order of how much they matter:
//
//  1. The SharedKey and Cosmos master-key signers produce byte-exact
//     signatures against fixed vectors — a canonicalization drift signs
//     garbage and every dropdown fails closed against the real service.
//  2. Host-building inputs (account/service/resource names) are pinned to
//     ^[a-zA-Z0-9-]{1,90}$ before URL interpolation, and custom endpoints
//     must parse as clean http(s) URLs (userinfo stripped, path kept for
//     Azurite).
//  3. The dial Control refuses link-local + cloud-metadata destinations, and
//     redirects are refused across hosts — the endpoints are caller-supplied.
//  4. Every failure is HTTP 200 + {"error": …} (the option-proxy convention),
//     and a ${credentials.X} / unresolvable ${secrets.X} reference fails
//     closed rather than being sent upstream as a literal.
//  5. Each proxy speaks its provider's actual wire format (auth header
//     placement, envelope, option labels), verified against httptest fakes.
//  6. The dropdown wiring in dynamicOptionsMetadata matches the spec's action
//     tables: the right markers exist with the right params, and list-actions
//     without the input get none.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupAzureRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/action/options/azure-storage-containers", svc.getAzureStorageContainers)
	r.GET("/api/v1/action/options/azure-cosmos-databases", svc.getAzureCosmosDatabases)
	r.GET("/api/v1/action/options/azure-cosmos-containers", svc.getAzureCosmosContainers)
	r.GET("/api/v1/action/options/azure-entra-groups", svc.getAzureEntraGroups)
	r.GET("/api/v1/action/options/azure-entra-users", svc.getAzureEntraUsers)
	r.GET("/api/v1/action/options/azure-openai-deployments", svc.getAzureOpenAIDeployments)
	r.GET("/api/v1/action/options/azure-aisearch-indexes", svc.getAzureAISearchIndexes)
	return r
}

// getAzureOptions calls one of the seven endpoints and returns the decoded
// body plus the status code, so every test can assert the 200-always
// convention.
func getAzureOptions(r *gin.Engine, endpoint string, params map[string]string) (map[string]any, int) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/azure-"+endpoint+"?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body, rec.Code
}

// Option payloads are flattened with the package-wide optionNames helper
// (awx_options_test.go).

// ---------------------------------------------------------------------------
// 1. Signing — fixed vectors.
// ---------------------------------------------------------------------------

// Fixed key material for the vectors: base64("storage-key-vector") and
// base64("cosmos-master-key-vector"). The expected signatures were computed
// independently with crypto/hmac over the hand-written canonical strings.
const (
	azureTestStorageKeyB64 = "c3RvcmFnZS1rZXktdmVjdG9y"
	azureTestCosmosKeyB64  = "Y29zbW9zLW1hc3Rlci1rZXktdmVjdG9y"
	azureTestDate          = "Wed, 16 Jul 2026 10:00:00 GMT"
)

func TestAzureStorageSharedKeySigning_FixedVector(t *testing.T) {
	g := NewWithT(t)

	req, err := http.NewRequest(http.MethodGet, "https://myaccount.blob.core.windows.net/?comp=list&maxresults=500", nil)
	g.Expect(err).To(BeNil())
	req.Header.Set("x-ms-date", azureTestDate)
	req.Header.Set("x-ms-version", "2023-11-03")

	// The canonical string, per the OFFICIAL slot order: VERB, then eleven
	// empty standard-header slots (Content-Encoding FIRST — n8n swaps
	// Content-Encoding/Content-Language and only survives because both are
	// empty), then the sorted x-ms headers, then "/{account}{path}" and the
	// sorted decoded query parameters.
	wantSTS := "GET\n\n\n\n\n\n\n\n\n\n\n\n" +
		"x-ms-date:" + azureTestDate + "\n" +
		"x-ms-version:2023-11-03\n" +
		"/myaccount/\ncomp:list\nmaxresults:500"
	g.Expect(azureStorageStringToSign("myaccount", req)).To(Equal(wantSTS))

	auth, err := azureStorageSharedKeyAuth("myaccount", azureTestStorageKeyB64, req)
	g.Expect(err).To(BeNil())
	g.Expect(auth).To(Equal("SharedKey myaccount:fc0XAk+r+pn13G7qsiPFASVdpRV1rwVSmtfpTE6Nfw8="))
}

// Azurite-style endpoints carry the account as a path segment, and Microsoft's
// documented emulator rule doubles it in CanonicalizedResource (once as the
// account, once in the path).
func TestAzureStorageSharedKeySigning_AzuriteDoublesTheAccount(t *testing.T) {
	g := NewWithT(t)

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:10000/devstoreaccount1/?comp=list&maxresults=500", nil)
	g.Expect(err).To(BeNil())
	req.Header.Set("x-ms-date", azureTestDate)
	req.Header.Set("x-ms-version", "2023-11-03")

	g.Expect(azureStorageStringToSign("devstoreaccount1", req)).To(ContainSubstring("/devstoreaccount1/devstoreaccount1/"))

	auth, err := azureStorageSharedKeyAuth("devstoreaccount1", azureTestStorageKeyB64, req)
	g.Expect(err).To(BeNil())
	g.Expect(auth).To(Equal("SharedKey devstoreaccount1:Yj/NMhK8HFy45QLPuhKoD8xFSzejBtvvQ/9p4c6REJg="))
}

func TestAzureStorageSharedKeyAuth_RefusesBadBase64(t *testing.T) {
	g := NewWithT(t)
	req, _ := http.NewRequest(http.MethodGet, "https://a.blob.core.windows.net/?comp=list", nil)
	_, err := azureStorageSharedKeyAuth("a", "!!!!", req)
	g.Expect(err).To(HaveOccurred())
	_, err = azureStorageSharedKeyAuth("a", "", req)
	g.Expect(err).To(HaveOccurred())
}

func TestAzureCosmosMasterKeySigning_FixedVectors(t *testing.T) {
	g := NewWithT(t)

	// dbs list: payload "get\ndbs\n\n{lowercased date}\n\n" — verb/type/date
	// lowercased, resourceId empty, ending with the empty second-date line.
	auth, err := azureCosmosMasterKeyAuth(http.MethodGet, "dbs", "", azureTestDate, azureTestCosmosKeyB64)
	g.Expect(err).To(BeNil())
	g.Expect(auth).To(Equal(url.QueryEscape("type=master&ver=1.0&sig=H6pc+pTA+XkDMzB7p4f1FvkFr+ThNw/LzGFXZDVcilk=")))
	// The header value must be URL-encoded ('=' and '&' escaped), per the
	// Cosmos auth scheme.
	g.Expect(auth).To(HavePrefix("type%3Dmaster%26ver%3D1.0%26sig%3D"))

	// colls list: resourceId is the RAW parent path "dbs/{db}" — NOT
	// lowercased, NOT URL-encoded.
	auth, err = azureCosmosMasterKeyAuth(http.MethodGet, "colls", "dbs/flo-db", azureTestDate, azureTestCosmosKeyB64)
	g.Expect(err).To(BeNil())
	g.Expect(auth).To(Equal(url.QueryEscape("type=master&ver=1.0&sig=tSyjSSxp6lgdaiaG8dFBGoeUp1yZorP1BLSYjeP0Z7c=")))

	_, err = azureCosmosMasterKeyAuth(http.MethodGet, "dbs", "", azureTestDate, "!!!!")
	g.Expect(err).To(HaveOccurred())
}

// ---------------------------------------------------------------------------
// 2. Host-building input validation.
// ---------------------------------------------------------------------------

func TestAzureNamePattern(t *testing.T) {
	g := NewWithT(t)

	for _, name := range []string{"myaccount", "my-account-1", "A1", "devstoreaccount1"} {
		g.Expect(azureNamePattern.MatchString(name)).To(BeTrue(), "name %q must be accepted", name)
	}
	for _, name := range []string{
		"", "my_account", "my account", "acc/../x", "${secrets.ACCOUNT}",
		"a.example.com", "café", "acc?x=1", strings.Repeat("a", 91),
	} {
		g.Expect(azureNamePattern.MatchString(name)).To(BeFalse(), "name %q must be refused — it is interpolated into a hostname", name)
	}
}

func TestAzureOptionsBaseURL(t *testing.T) {
	g := NewWithT(t)

	for _, tc := range []struct{ raw, want string }{
		{"https://localhost:8081", "https://localhost:8081"},
		{"https://localhost:8081/", "https://localhost:8081"},
		{"http://127.0.0.1:10000/devstoreaccount1", "http://127.0.0.1:10000/devstoreaccount1"},
		{"http://127.0.0.1:10000/devstoreaccount1/", "http://127.0.0.1:10000/devstoreaccount1"},
		{"myaccount.blob.core.windows.net", "https://myaccount.blob.core.windows.net"},
		// Userinfo is stripped so a crafted endpoint can't smuggle credentials
		// into the server-side request.
		{"https://user:pass@host.example/path", "https://host.example/path"},
	} {
		got, err := azureOptionsBaseURL(tc.raw)
		g.Expect(err).To(BeNil(), "endpoint %q must be accepted", tc.raw)
		g.Expect(got).To(Equal(tc.want), "endpoint %q", tc.raw)
	}

	for _, raw := range []string{"", "${secrets.ENDPOINT}", "ftp://host", "://"} {
		_, err := azureOptionsBaseURL(raw)
		g.Expect(err).To(HaveOccurred(), "endpoint %q must be refused", raw)
	}
}

// The Graph base must normalise identically to the executor's entra
// normaliseEndpoint — otherwise a graph_endpoint the node accepts makes the
// dropdown 404 while the action succeeds.
func TestAzureGraphBaseURL_MirrorsTheExecutorNormalisation(t *testing.T) {
	g := NewWithT(t)

	for _, tc := range []struct{ raw, want string }{
		{"https://graph.microsoft.us", "https://graph.microsoft.us"},
		{"graph.microsoft.us", "https://graph.microsoft.us"},
		{"https://graph.microsoft.us/", "https://graph.microsoft.us"},
		{"https://graph.microsoft.com/v1.0", "https://graph.microsoft.com"},
		{"https://graph.microsoft.com/v1.0/", "https://graph.microsoft.com"},
		{"graph.microsoft.com/v1.0", "https://graph.microsoft.com"},
		// Only ONE version suffix is stripped, exactly as the executor does.
		{"https://graph.microsoft.com/v1.0/v1.0", "https://graph.microsoft.com/v1.0"},
		// A non-version path prefix is kept (a reverse proxy in front of Graph).
		{"https://proxy.example/graph", "https://proxy.example/graph"},
		{"https://proxy.example/graph/v1.0/", "https://proxy.example/graph"},
		// Userinfo stripping still applies.
		{"https://user:pass@graph.example/v1.0", "https://graph.example"},
	} {
		got, err := azureGraphBaseURL(tc.raw)
		g.Expect(err).To(BeNil(), "graph_endpoint %q must be accepted", tc.raw)
		g.Expect(got).To(Equal(tc.want), "graph_endpoint %q", tc.raw)
	}

	for _, raw := range []string{"", "${secrets.GRAPH}", "ftp://host/v1.0"} {
		_, err := azureGraphBaseURL(raw)
		g.Expect(err).To(HaveOccurred(), "graph_endpoint %q must be refused", raw)
	}
}

// ---------------------------------------------------------------------------
// 3. SSRF guards.
// ---------------------------------------------------------------------------

func TestAzureOptionsDialControl(t *testing.T) {
	g := NewWithT(t)

	// Refused: link-local (the cloud metadata service lives at
	// 169.254.169.254) and the metadata addresses outside that range.
	for _, addr := range []string{
		"169.254.169.254:443",
		"169.254.0.1:80",
		"[fe80::1]:443",
		"[fd00:ec2::254]:443", // AWS IMDS over IPv6
		"100.100.100.200:443", // Alibaba Cloud
	} {
		g.Expect(azureOptionsDialControl("tcp", addr, nil)).To(HaveOccurred(), "must refuse %s", addr)
	}

	// Allowed: loopback and RFC1918 — the Azurite and Cosmos emulators live
	// there — plus ordinary public addresses.
	for _, addr := range []string{
		"127.0.0.1:10000",
		"192.168.1.169:8081",
		"10.0.0.5:443",
		"[::1]:10000",
		"20.38.122.1:443",
	} {
		g.Expect(azureOptionsDialControl("tcp", addr, nil)).To(BeNil(), "must allow %s", addr)
	}
}

func TestAzureOptionsRedirectRefusal(t *testing.T) {
	g := NewWithT(t)

	first, _ := http.NewRequest(http.MethodGet, "https://myaccount.blob.core.windows.net/", nil)
	sameHost, _ := http.NewRequest(http.MethodGet, "https://myaccount.blob.core.windows.net/other", nil)
	otherHost, _ := http.NewRequest(http.MethodGet, "https://evil.example/", nil)

	g.Expect(azureOptionsRedirect(sameHost, []*http.Request{first})).To(BeNil())
	g.Expect(azureOptionsRedirect(otherHost, []*http.Request{first})).To(HaveOccurred(), "cross-host redirects must be refused")
	g.Expect(azureOptionsRedirect(sameHost, []*http.Request{first, first, first, first, first})).To(HaveOccurred(), "redirect chains must be bounded")
}

// ---------------------------------------------------------------------------
// 4. Failure convention + credential guards (always HTTP 200 + {"error": …}).
// ---------------------------------------------------------------------------

func TestAzureOptionsInputFailuresAre200WithError(t *testing.T) {
	g := NewWithT(t)
	r := setupAzureRouter(&Service{})

	for _, tc := range []struct {
		endpoint string
		params   map[string]string
		want     string
	}{
		{"storage-containers", map[string]string{}, "Storage Account"},
		{"storage-containers", map[string]string{"account_name": "bad_name!"}, "Storage Account"},
		{"storage-containers", map[string]string{"account_name": "acct", "endpoint": "ftp://x"}, "Custom Endpoint"},
		{"storage-containers", map[string]string{"account_name": "acct", "auth_method": "banana"}, "Authentication method"},
		{"storage-containers", map[string]string{"account_name": "acct"}, "Account Key"},
		{"storage-containers", map[string]string{"account_name": "acct", "account_key": "${credentials.KEY}"}, "Managed credentials"},
		{"storage-containers", map[string]string{"account_name": "acct", "account_key": "${secrets.KEY}"}, "Select an environment"},
		{"storage-containers", map[string]string{"account_name": "acct", "auth_method": "entra"}, "Tenant ID"},
		{"storage-containers", map[string]string{"account_name": "acct", "auth_method": "entra", "azure_tenant_id": "t"}, "Client ID"},
		{"storage-containers", map[string]string{"account_name": "acct", "auth_method": "entra", "azure_tenant_id": "t", "azure_client_id": "c"}, "Client Secret"},
		{"cosmos-databases", map[string]string{}, "Account Name"},
		{"cosmos-databases", map[string]string{"account_name": "acct"}, "Master Key"},
		{"cosmos-databases", map[string]string{"account_name": "acct", "auth_method": "banana"}, "Authentication method"},
		{"cosmos-containers", map[string]string{"account_name": "acct", "master_key": azureTestCosmosKeyB64}, "Select a Database"},
		{"cosmos-containers", map[string]string{"account_name": "acct", "master_key": azureTestCosmosKeyB64, "database": "${flow.db}"}, "Select a Database"},
		{"entra-groups", map[string]string{}, "Tenant ID"},
		{"entra-users", map[string]string{"azure_tenant_id": "t", "azure_client_id": "c", "azure_client_secret": "${credentials.X}"}, "Managed credentials"},
		{"openai-deployments", map[string]string{}, "Resource Name or Custom Endpoint"},
		{"openai-deployments", map[string]string{"resource_name": "bad_name!"}, "Resource Name"},
		{"aisearch-indexes", map[string]string{}, "Service Name or Custom Endpoint"},
		{"aisearch-indexes", map[string]string{"service_name": "bad_name!"}, "Service Name"},
		{"aisearch-indexes", map[string]string{"service_name": "svc"}, "API Key"},
	} {
		body, code := getAzureOptions(r, tc.endpoint, tc.params)
		g.Expect(code).To(Equal(http.StatusOK), "%s %v — option proxies always answer 200", tc.endpoint, tc.params)
		g.Expect(body).To(HaveKey("error"), "%s %v", tc.endpoint, tc.params)
		g.Expect(body["error"]).To(ContainSubstring(tc.want), "%s %v", tc.endpoint, tc.params)
	}
}

// A bad-base64 key must be caught before anything is dialled.
func TestAzureStorageContainers_RefusesBadBase64Key(t *testing.T) {
	g := NewWithT(t)
	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "storage-containers", map[string]string{
		"account_name": "acct",
		"account_key":  "!!!!",
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body["error"]).To(ContainSubstring("not valid base64"))
}

// ---------------------------------------------------------------------------
// 5. Wire formats, against httptest fakes.
// ---------------------------------------------------------------------------

func TestAzureStorageContainersOptions_SharedKey(t *testing.T) {
	g := NewWithT(t)

	var gotAuth, gotVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotVersion = req.Header.Get("x-ms-version")
		gotPath = req.URL.Path
		g.Expect(req.URL.Query().Get("comp")).To(Equal("list"))
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ServiceEndpoint="http://x/">
  <Containers>
    <Container><Name>zeta</Name><Properties/></Container>
    <Container><Name>alpha</Name><Properties/></Container>
  </Containers>
  <NextMarker/>
</EnumerationResults>`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "storage-containers", map[string]string{
		"account_name": "devstoreaccount1",
		"auth_method":  "shared_key",
		"account_key":  azureTestStorageKeyB64,
		"endpoint":     srv.URL,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"alpha", "zeta"}), "options must be sorted by name")
	g.Expect(gotAuth).To(HavePrefix("SharedKey devstoreaccount1:"), "the list call must be SharedKey-signed server-side")
	g.Expect(gotVersion).To(Equal("2023-11-03"))
	g.Expect(gotPath).To(Equal("/"))
}

func TestAzureCosmosDatabasesOptions_MasterKey(t *testing.T) {
	g := NewWithT(t)

	var gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.URL.Path).To(Equal("/dbs"))
		gotAuth = req.Header.Get("Authorization")
		gotVersion = req.Header.Get("x-ms-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_rid":"","Databases":[{"id":"beta","_rid":"x"},{"id":"alpha","_rid":"y"}],"_count":2}`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "cosmos-databases", map[string]string{
		"account_name": "mycosmos",
		"master_key":   azureTestCosmosKeyB64,
		"endpoint":     srv.URL,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"alpha", "beta"}))
	// The authorization value is URL-encoded "type=master&ver=1.0&sig=…".
	g.Expect(gotAuth).To(HavePrefix("type%3Dmaster%26ver%3D1.0%26sig%3D"))
	g.Expect(gotVersion).To(Equal("2018-12-31"))
}

func TestAzureCosmosContainersOptions_MasterKey(t *testing.T) {
	g := NewWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.URL.Path).To(Equal("/dbs/flo-db/colls"))
		g.Expect(req.Header.Get("Authorization")).To(HavePrefix("type%3Dmaster%26"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_rid":"x","DocumentCollections":[{"id":"orders"},{"id":"customers"}],"_count":2}`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "cosmos-containers", map[string]string{
		"account_name": "mycosmos",
		"master_key":   azureTestCosmosKeyB64,
		"endpoint":     srv.URL,
		"database":     "flo-db",
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"customers", "orders"}))
}

// The Entra path of the Cosmos proxy: the client-credentials token is minted
// from azureLoginBase (the test seam) and carried as the URL-encoded
// "type=aad" authorization value.
func TestAzureCosmosDatabasesOptions_EntraAuth(t *testing.T) {
	g := NewWithT(t)

	var gotGrant, gotScope, gotClientID string
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.URL.Path).To(Equal("/test-tenant/oauth2/v2.0/token"))
		g.Expect(req.ParseForm()).To(BeNil())
		gotGrant = req.PostForm.Get("grant_type")
		gotScope = req.PostForm.Get("scope")
		gotClientID = req.PostForm.Get("client_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3599}`))
	}))
	defer login.Close()

	oldLogin := azureLoginBase
	azureLoginBase = login.URL
	defer func() { azureLoginBase = oldLogin }()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Databases":[{"id":"emulator-db"}],"_count":1}`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "cosmos-databases", map[string]string{
		"account_name":        "mycosmos",
		"auth_method":         "entra",
		"azure_tenant_id":     "test-tenant",
		"azure_client_id":     "client-1",
		"azure_client_secret": "sp-secret",
		"endpoint":            srv.URL,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"emulator-db"}))
	g.Expect(gotGrant).To(Equal("client_credentials"))
	g.Expect(gotClientID).To(Equal("client-1"))
	// The AAD scope is derived from the endpoint host.
	g.Expect(gotScope).To(HaveSuffix("/.default"))
	g.Expect(gotScope).To(ContainSubstring("127.0.0.1"))
	g.Expect(gotAuth).To(Equal(url.QueryEscape("type=aad&ver=1.0&sig=test-token")))
}

func TestAzureEntraGroupsAndUsersOptions(t *testing.T) {
	g := NewWithT(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/test-tenant/oauth2/v2.0/token", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"graph-token"}`))
	})
	mux.HandleFunc("/v1.0/groups", func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.Header.Get("Authorization")).To(Equal("Bearer graph-token"))
		g.Expect(req.Header.Get("ConsistencyLevel")).To(Equal("eventual"))
		g.Expect(req.URL.Query().Get("$select")).To(Equal("id,displayName"))
		g.Expect(req.URL.Query().Get("$top")).To(Equal("100"))
		g.Expect(req.URL.Query().Get("$orderby")).To(Equal("displayName"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"@odata.count":2,"value":[{"id":"g2","displayName":"Zeta Team"},{"id":"g1","displayName":"Alpha Team"}]}`))
	})
	mux.HandleFunc("/v1.0/users", func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.Header.Get("Authorization")).To(Equal("Bearer graph-token"))
		g.Expect(req.URL.Query().Get("$select")).To(Equal("id,displayName,userPrincipalName"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"u1","displayName":"Dana Scully","userPrincipalName":"dana@contoso.com"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldLogin := azureLoginBase
	azureLoginBase = srv.URL
	defer func() { azureLoginBase = oldLogin }()

	params := map[string]string{
		"azure_tenant_id":     "test-tenant",
		"azure_client_id":     "client-1",
		"azure_client_secret": "sp-secret",
		"graph_endpoint":      srv.URL,
	}
	r := setupAzureRouter(&Service{})

	body, code := getAzureOptions(r, "entra-groups", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"Alpha Team", "Zeta Team"}))

	body, code = getAzureOptions(r, "entra-users", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"Dana Scully (dana@contoso.com)"}))
	raw, _ := body["options"].([]any)
	first, _ := raw[0].(map[string]any)
	g.Expect(first["value"]).To(Equal("u1"), "the option value is the directory object id, not the UPN")
}

// The executor strips a /v1.0 suffix off graph_endpoint before appending its
// own, so an operator who pasted the versioned URL has a working node. The
// proxy must land on the same path rather than {host}/v1.0/v1.0.
func TestAzureEntraOptions_GraphEndpointWithVersionSuffix(t *testing.T) {
	g := NewWithT(t)

	var requested []string
	mux := http.NewServeMux()
	mux.HandleFunc("/test-tenant/oauth2/v2.0/token", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"graph-token"}`))
	})
	mux.HandleFunc("/v1.0/groups", func(w http.ResponseWriter, req *http.Request) {
		requested = append(requested, req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"g1","displayName":"Alpha Team"}]}`))
	})
	// Anything else is a normalisation slip; record it so the failure names the
	// path actually requested.
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		requested = append(requested, req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldLogin := azureLoginBase
	azureLoginBase = srv.URL
	defer func() { azureLoginBase = oldLogin }()

	r := setupAzureRouter(&Service{})
	for _, suffix := range []string{"", "/", "/v1.0", "/v1.0/"} {
		requested = nil
		body, code := getAzureOptions(r, "entra-groups", map[string]string{
			"azure_tenant_id":     "test-tenant",
			"azure_client_id":     "client-1",
			"azure_client_secret": "sp-secret",
			"graph_endpoint":      srv.URL + suffix,
		})
		g.Expect(code).To(Equal(http.StatusOK))
		g.Expect(body).ToNot(HaveKey("error"), "graph_endpoint suffix %q: %v (requested %v)", suffix, body["error"], requested)
		g.Expect(requested).To(Equal([]string{"/v1.0/groups"}), "graph_endpoint suffix %q", suffix)
		g.Expect(optionNames(body)).To(Equal([]string{"Alpha Team"}))
	}
}

func TestAzureOpenAIDeploymentsOptions(t *testing.T) {
	g := NewWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.URL.Path).To(Equal("/openai/deployments"))
		// The deployments listing only exists on this preview data-plane
		// version — the node's chat api_version must NOT be forwarded here.
		g.Expect(req.URL.Query().Get("api-version")).To(Equal("2023-03-15-preview"))
		g.Expect(req.Header.Get("api-key")).To(Equal("sk-test"), "Azure OpenAI auth is the api-key header, not Bearer")
		g.Expect(req.Header.Get("Authorization")).To(BeEmpty())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt4o-prod","model":"gpt-4o","status":"succeeded"},{"id":"embed-small","model":"text-embedding-3-small","status":"succeeded"}]}`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "openai-deployments", map[string]string{
		"api_key":  "sk-test",
		"endpoint": srv.URL,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"embed-small (text-embedding-3-small)", "gpt4o-prod (gpt-4o)"}))
	raw, _ := body["options"].([]any)
	first, _ := raw[0].(map[string]any)
	g.Expect(first["value"]).To(Equal("embed-small"), "the option value is the bare deployment id")
}

func TestAzureAISearchIndexesOptions(t *testing.T) {
	g := NewWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.Expect(req.URL.Path).To(Equal("/indexes"))
		g.Expect(req.URL.Query().Get("api-version")).To(Equal("2024-07-01"), "the executor's default api_version must be applied when the input is unset")
		g.Expect(req.URL.Query().Get("$select")).To(Equal("name"))
		g.Expect(req.Header.Get("api-key")).To(Equal("admin-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"name":"products-index"},{"name":"docs-index"}]}`))
	}))
	defer srv.Close()

	r := setupAzureRouter(&Service{})
	body, code := getAzureOptions(r, "aisearch-indexes", map[string]string{
		"api_key":  "admin-key",
		"endpoint": srv.URL,
	})
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).ToNot(HaveKey("error"), "got error: %v", body["error"])
	g.Expect(optionNames(body)).To(Equal([]string{"docs-index", "products-index"}))
}

// ---------------------------------------------------------------------------
// 6. Dropdown wiring.
// ---------------------------------------------------------------------------

func TestAzureDynamicOptionsRegistration(t *testing.T) {
	g := NewWithT(t)

	counts := map[string]int{}
	for key, marker := range dynamicOptionsMetadata {
		if !strings.HasPrefix(marker.Endpoint, "/api/v1/action/options/azure-") {
			continue
		}
		counts[strings.TrimPrefix(marker.Endpoint, "/api/v1/action/options/")]++
		_ = key
	}
	g.Expect(counts["azure-storage-containers"]).To(Equal(18), "every azure/storage action naming an existing container")
	g.Expect(counts["azure-cosmos-databases"]).To(Equal(16), "every azure/cosmosdb action naming an existing database")
	g.Expect(counts["azure-cosmos-containers"]).To(Equal(12), "every azure/cosmosdb action naming an existing container")
	g.Expect(counts["azure-entra-users"]).To(Equal(13), "12 user_id inputs + user_set_manager's manager_id")
	g.Expect(counts["azure-entra-groups"]).To(Equal(9))
	g.Expect(counts["azure-openai-deployments"]).To(Equal(1))
	g.Expect(counts["azure-aisearch-indexes"]).To(Equal(8), "every azureaisearch action naming an existing index")

	// Spot-check the params: the exact auth input names, plus the dependency
	// input on the cosmos containers picker.
	marker, ok := dynamicOptionsMetadata["azure/storage/blob_upload#container"]
	g.Expect(ok).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/azure-storage-containers"))
	g.Expect(marker.Params).To(Equal([]string{
		"account_name", "auth_method", "account_key",
		"azure_tenant_id", "azure_client_id", "azure_client_secret",
		"endpoint", "allow_insecure",
	}))

	marker, ok = dynamicOptionsMetadata["azure/cosmosdb/item_get#container"]
	g.Expect(ok).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/azure-cosmos-containers"))
	g.Expect(marker.Params).To(ContainElement("database"), "the containers picker depends on the chosen database")

	marker, ok = dynamicOptionsMetadata["azure/entra/user_set_manager#manager_id"]
	g.Expect(ok).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/azure-entra-users"))

	marker, ok = dynamicOptionsMetadata["ai/azure_openai#deployment"]
	g.Expect(ok).To(BeTrue())
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/azure-openai-deployments"))
	g.Expect(marker.Params).To(Equal([]string{"api_key", "resource_name", "endpoint"}))

	// The database a container is created in must already exist, so that
	// parent reference DOES get a picker even on a create action.
	marker, ok = dynamicOptionsMetadata["azure/cosmosdb/container_create#database"]
	g.Expect(ok).To(BeTrue(), "container_create's parent database is an existing resource")
	g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/azure-cosmos-databases"))

	// List-actions without the input must NOT get a marker (they list the very
	// resource the picker would ask them for), and neither must an input that
	// names a resource being created — a list of what already exists is no
	// help when you are typing a new name.
	for _, absent := range []string{
		"azure/storage/container_get_all#container",
		"azure/cosmosdb/database_get_all#database",
		"vectordatabase/azureaisearch/index_get_all#index_name",
		"azure/storage/container_create#container",
		"azure/cosmosdb/database_create#database",
		"azure/cosmosdb/container_create#container",
		"vectordatabase/azureaisearch/index_create#index_name",
	} {
		_, ok := dynamicOptionsMetadata[absent]
		g.Expect(ok).To(BeFalse(), "%s must not be registered", absent)
	}
}
