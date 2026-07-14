package http

// Tests for the pgvector options proxy — the one option resolver that opens a
// raw Postgres connection to a caller-named host rather than speaking HTTP.
//
// The invariants under test, in rough order of how much they matter:
//
//  1. An EMPTY host is refused before anything is dialled. This is the whole
//     ballgame: libpq defaults to host=localhost port=5432, so an empty host
//     would connect this proxy to the api's OWN control-plane Postgres and list
//     the platform's internal schema back to any logged-in user.
//  2. A host containing '/' (a unix-socket directory) is refused, as is any host
//     outside ^[A-Za-z0-9._:\[\]-]+$ — the characters that let a crafted host
//     smuggle extra libpq connection parameters.
//  3. The DSN is assembled by net/url, so a password full of '@' and '/' cannot
//     restructure it, and sslmode is pinned to libpq's six legal values.
//  4. The dial Control hook ACTUALLY FIRES through pq.NewConnector + sql.OpenDB.
//     Verified two ways: on the dialer directly, and end-to-end through the
//     connector openPGVectorDB builds, both aimed at 169.254.169.254.
//  5. Every failure is HTTP 200 + {"error": …}, and the raw pq error (which
//     carries server internals) never reaches the client.
//  6. The dropdown wiring matches the manifest: table_create gets a Schema
//     picker but NOT a Table picker, and no action gets a picker for an input it
//     does not have.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func setupPGVectorRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	})
	r.GET("/api/v1/action/options/pgvector-schemas", svc.getPGVectorSchemas)
	r.GET("/api/v1/action/options/pgvector-tables", svc.getPGVectorTables)
	r.GET("/api/v1/action/options/pgvector-columns", svc.getPGVectorColumns)
	return r
}

// getPGVectorOptions calls one of the three endpoints and returns the decoded
// body plus the status code, so every test can assert the 200-always convention.
func getPGVectorOptions(r *gin.Engine, endpoint string, params map[string]string) (map[string]any, int) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action/options/pgvector-"+endpoint+"?"+q.Encode(), nil)
	r.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body, rec.Code
}

// goodPGParams is a well-formed connection to a host that does not answer, used
// by the tests that want to get PAST validation.
func goodPGParams() map[string]string {
	return map[string]string{
		"host":     "192.0.2.1", // TEST-NET-1, guaranteed unroutable
		"port":     "5432",
		"database": "vectordb",
		"username": "flomation",
		"password": "hunter2",
		"ssl_mode": "disable",
	}
}

// ---------------------------------------------------------------------------
// 1 + 2. Host validation — the control that stops the proxy reaching the api's
//        own database.
// ---------------------------------------------------------------------------

func TestPGVectorHostValidation(t *testing.T) {
	g := NewWithT(t)

	// Hosts that must be REFUSED, and the substring the operator should see.
	for _, tc := range []struct {
		host string
		want string
		why  string
	}{
		{"", "Database Host", "empty → libpq falls back to host=localhost: the api's OWN Postgres"},
		{"   ", "Database Host", "whitespace-only is empty"},
		{"/var/run/postgresql", "socket directory", "a '/' host is a unix-socket directory — the api's own socket"},
		{"/tmp", "socket directory", "unix-socket directory"},
		{"localhost/../x", "socket directory", "any '/' at all"},
		{"evil?options=-c", "connection parameters", "'?' could smuggle libpq parameters"},
		{"host options=-c search_path=x", "connection parameters", "a space starts a new libpq keyword"},
		{"db host=127.0.0.1", "connection parameters", "space + '=' smuggles a second host"},
		{"db,other", "connection parameters", "',' is libpq's multi-host separator"},
		{"user@db.example.com", "connection parameters", "'@' would restructure the URL userinfo"},
		{"postgres://db.example.com", "socket directory", "a scheme contains '/'"},
		{"db.example.com:5432", "must not include a port", "port belongs in the Port field"},
		{"${secrets.DB_HOST}", "variable", "an unresolved reference is not a host"},
	} {
		got, msg := validatePGVectorHost(tc.host)
		g.Expect(msg).To(Not(BeEmpty()), "host %q must be refused (%s)", tc.host, tc.why)
		g.Expect(got).To(BeEmpty(), "host %q must not yield a usable host", tc.host)
		g.Expect(msg).To(ContainSubstring(tc.want), "host %q: message was %q", tc.host, msg)
	}

	// Hosts that must be ACCEPTED. Loopback and RFC1918 stay allowed on purpose:
	// customers self-host their database on a LAN, exactly as with Jenkins and
	// Kubernetes. 169.254.169.254 passes *validation* — it is the DIAL that
	// refuses it (see TestPGVectorDialControl below), because the guard has to
	// run on the address actually dialled to also catch a DNS name pointing there.
	for _, tc := range []struct{ host, want string }{
		{"db.example.com", "db.example.com"},
		{"192.168.80.20", "192.168.80.20"},
		{"10.0.0.5", "10.0.0.5"},
		{"127.0.0.1", "127.0.0.1"},
		{"localhost", "localhost"},
		{"my-db-1.internal", "my-db-1.internal"},
		{"  db.example.com  ", "db.example.com"},
		{"169.254.169.254", "169.254.169.254"}, // refused at dial, not here
		{"::1", "::1"},
		{"[::1]", "::1"}, // brackets stripped; JoinHostPort re-adds them
		{"[fd00::1]", "fd00::1"},
	} {
		got, msg := validatePGVectorHost(tc.host)
		g.Expect(msg).To(BeEmpty(), "host %q must be accepted, got %q", tc.host, msg)
		g.Expect(got).To(Equal(tc.want), "host %q", tc.host)
	}
}

// ---------------------------------------------------------------------------
// 3. DSN construction.
// ---------------------------------------------------------------------------

func TestPGVectorDSN(t *testing.T) {
	g := NewWithT(t)

	for _, tc := range []struct {
		name string
		conn pgProxyConn
		want string
	}{
		{
			name: "ordinary",
			conn: pgProxyConn{Host: "192.168.80.20", Port: 5432, Database: "vectordb", Username: "flomation", Password: "s3cret", SSLMode: "disable"},
			want: "postgres://flomation:s3cret@192.168.80.20:5432/vectordb?connect_timeout=5&sslmode=disable",
		},
		{
			// The reason the DSN is built with net/url and not fmt.Sprintf: a
			// password containing '@' and '/' must be escaped, not allowed to
			// restructure the URL into a different host.
			name: "password full of URL metacharacters",
			conn: pgProxyConn{Host: "db.example.com", Port: 5432, Database: "vectordb", Username: "flomation", Password: "p@ss/w:rd?#&=", SSLMode: "require"},
			want: "postgres://flomation:p%40ss%2Fw%3Ard%3F%23&=@db.example.com:5432/vectordb?connect_timeout=5&sslmode=require",
		},
		{
			name: "username with metacharacters",
			conn: pgProxyConn{Host: "db", Port: 5432, Database: "d", Username: "a@b/c", Password: "x", SSLMode: "verify-full"},
			want: "postgres://a%40b%2Fc:x@db:5432/d?connect_timeout=5&sslmode=verify-full",
		},
		{
			name: "ipv6 literal is bracketed",
			conn: pgProxyConn{Host: "fd00::1", Port: 5433, Database: "vectordb", Username: "u", Password: "p", SSLMode: "prefer"},
			want: "postgres://u:p@[fd00::1]:5433/vectordb?connect_timeout=5&sslmode=prefer",
		},
		{
			name: "non-default port",
			conn: pgProxyConn{Host: "db", Port: 15432, Database: "d", Username: "u", Password: "p", SSLMode: "verify-ca"},
			want: "postgres://u:p@db:15432/d?connect_timeout=5&sslmode=verify-ca",
		},
	} {
		got := tc.conn.dsn()
		g.Expect(got).To(Equal(tc.want), "case: %s", tc.name)

		// Whatever else it does, a DSN must always bound the dial and must never
		// carry an `options=` parameter (which can set arbitrary server GUCs).
		g.Expect(got).To(ContainSubstring("connect_timeout=5"), "case: %s", tc.name)
		g.Expect(got).To(Not(ContainSubstring("options=")), "case: %s", tc.name)

		// The DSN must round-trip to exactly the host we validated — no smuggled
		// second host, no path.
		u, err := url.Parse(got)
		g.Expect(err).To(BeNil(), "case: %s", tc.name)
		g.Expect(u.Query()).To(HaveLen(2), "case: %s — only sslmode + connect_timeout", tc.name)
		g.Expect(u.Query().Get("sslmode")).To(Equal(tc.conn.SSLMode), "case: %s", tc.name)
	}
}

// sslmode must be pinned to libpq's closed six-value set — an unknown keyword
// must never reach the connection string.
func TestPGVectorSSLModeAllowList(t *testing.T) {
	g := NewWithT(t)
	g.Expect(pgValidSSLModes).To(HaveLen(6))
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		g.Expect(pgValidSSLModes).To(HaveKey(mode))
	}

	r := setupPGVectorRouter(&Service{})
	params := goodPGParams()
	params["ssl_mode"] = "verify-none-lol"
	body, code := getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("SSL Mode"))
}

// ---------------------------------------------------------------------------
// 4. The dial Control hook actually fires.
// ---------------------------------------------------------------------------

// The guard, at the level of the Control callback itself.
func TestPGVectorDialControl(t *testing.T) {
	g := NewWithT(t)

	// Refused: link-local (the cloud metadata service lives at 169.254.169.254)
	// and the metadata addresses that sit outside the link-local range.
	for _, addr := range []string{
		"169.254.169.254:5432",
		"169.254.0.1:5432",
		"[fe80::1]:5432",
		"[fd00:ec2::254]:5432", // AWS IMDS over IPv6
		"100.100.100.200:5432", // Alibaba Cloud
	} {
		g.Expect(pgvectorDialControl("tcp", addr, nil)).To(HaveOccurred(), "must refuse %s", addr)
	}

	// Allowed: loopback and RFC1918. Customers self-host their database on a LAN
	// — blocking these would break the node for its actual users.
	for _, addr := range []string{
		"127.0.0.1:5432",
		"192.168.80.20:5432",
		"10.0.0.5:5432",
		"172.16.0.1:5432",
		"[::1]:5432",
		"93.184.216.34:5432",
	} {
		g.Expect(pgvectorDialControl("tcp", addr, nil)).To(BeNil(), "must allow %s", addr)
	}
}

// The guard, at the level of the dialer pq is handed. Both the DialContext path
// (which pq prefers) and the DialTimeout fallback must run Control.
func TestPGVectorDialer_RefusesMetadataIP(t *testing.T) {
	g := NewWithT(t)
	d := newPGVectorDialer()

	start := time.Now()
	_, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:5432")
	g.Expect(err).To(HaveOccurred())
	// The message can only have come from the Control hook — which is the point:
	// it proves the hook ran, rather than the dial merely having timed out.
	g.Expect(err.Error()).To(ContainSubstring("link-local"))
	g.Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second), "must be refused immediately, not dialled and timed out")

	_, err = d.DialTimeout("tcp", "169.254.169.254:5432", 5*time.Second)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("link-local"))

	_, err = d.Dial("tcp", "169.254.169.254:5432")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("link-local"))
}

// The guard, end to end: through the *sql.DB that openPGVectorDB actually hands
// the handlers. This is the test that proves the Control hook survives being
// plumbed through pq.NewConnector + sql.OpenDB — if the connector's Dialer were
// never set, this connection would be attempted and would fail with a timeout or
// a network error instead, and the assertion on the message would catch it.
func TestOpenPGVectorDB_DialControlFiresThroughConnector(t *testing.T) {
	g := NewWithT(t)

	db, err := openPGVectorDB(pgProxyConn{
		Host: "169.254.169.254", Port: 5432,
		Database: "vectordb", Username: "u", Password: "p", SSLMode: "disable",
	})
	g.Expect(err).To(BeNil())
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err = db.PingContext(ctx)
	g.Expect(err).To(HaveOccurred(), "the dial to the metadata address must be refused")
	g.Expect(err.Error()).To(ContainSubstring("link-local"),
		"the refusal must come from our Control hook — got %q", err)
	g.Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second), "must be refused at dial, not timed out")
}

// ---------------------------------------------------------------------------
// 5. The 200 + {"error"} convention, and error hygiene.
// ---------------------------------------------------------------------------

// The single most important test in the file. An empty host must be refused
// OUTRIGHT: libpq would otherwise default to host=localhost port=5432 and this
// proxy would happily enumerate the api's own control-plane database.
func TestGetPGVectorSchemas_EmptyHostRefused(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	for _, endpoint := range []string{"schemas", "tables", "columns"} {
		params := goodPGParams()
		delete(params, "host") // omitted entirely
		body, code := getPGVectorOptions(r, endpoint, params)
		g.Expect(code).To(Equal(http.StatusOK), "endpoint: %s", endpoint)
		g.Expect(body).To(HaveKey("error"), "endpoint: %s — an omitted host MUST NOT dial localhost", endpoint)
		g.Expect(body["error"]).To(ContainSubstring("Database Host"), "endpoint: %s", endpoint)
		g.Expect(body).To(Not(HaveKey("options")), "endpoint: %s", endpoint)

		params["host"] = "" // present but empty
		body, code = getPGVectorOptions(r, endpoint, params)
		g.Expect(code).To(Equal(http.StatusOK), "endpoint: %s", endpoint)
		g.Expect(body).To(HaveKey("error"), "endpoint: %s — an empty host MUST NOT dial localhost", endpoint)
	}
}

func TestGetPGVectorOptions_HostValidationThroughHandler(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	for _, host := range []string{
		"/var/run/postgresql", // unix-socket directory → the api's own socket
		"evil?options=-c",     // smuggled libpq parameter
		"db host=127.0.0.1",   // smuggled second host
		"user@db.example.com", // userinfo
	} {
		params := goodPGParams()
		params["host"] = host
		body, code := getPGVectorOptions(r, "schemas", params)
		g.Expect(code).To(Equal(http.StatusOK), "host: %q", host)
		g.Expect(body).To(HaveKey("error"), "host: %q must be refused", host)
		g.Expect(body).To(Not(HaveKey("options")), "host: %q", host)
	}
}

// 169.254.169.254 passes host *validation* (it is a syntactically fine IP) and is
// stopped at the DIAL. Through the handler that surfaces as the ordinary
// 200 + {"error"} convention — never a 5xx, and never the raw dial error.
func TestGetPGVectorSchemas_MetadataIPRefusedAtDial(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	params := goodPGParams()
	params["host"] = "169.254.169.254"

	start := time.Now()
	body, code := getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body).To(Not(HaveKey("options")))
	g.Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second), "refused at dial, not timed out")

	// The client is told it could not be reached — not *why*, and never in the
	// driver's words.
	msg := body["error"].(string)
	g.Expect(msg).To(ContainSubstring("Could not reach the database"))
	g.Expect(strings.ToLower(msg)).To(Not(ContainSubstring("link-local")), "the guard must not be advertised to the client")
	g.Expect(msg).To(Not(ContainSubstring("pq:")), "the raw driver error must never reach the client")
}

// Missing connection fields each get their own message, and none of them dial.
func TestGetPGVectorOptions_MissingFields(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	for field, want := range map[string]string{
		"database": "Database",
		"username": "Username",
		"password": "Password",
	} {
		params := goodPGParams()
		delete(params, field)
		body, code := getPGVectorOptions(r, "schemas", params)
		g.Expect(code).To(Equal(http.StatusOK), "field: %s", field)
		g.Expect(body).To(HaveKey("error"), "field: %s", field)
		g.Expect(body["error"]).To(ContainSubstring(want), "field: %s", field)
	}

	// A port outside 1–65535 (or not a number) is refused rather than defaulted.
	for _, port := range []string{"0", "70000", "-1", "abc"} {
		params := goodPGParams()
		params["port"] = port
		body, code := getPGVectorOptions(r, "schemas", params)
		g.Expect(code).To(Equal(http.StatusOK), "port: %s", port)
		g.Expect(body).To(HaveKey("error"), "port: %s", port)
		g.Expect(body["error"]).To(ContainSubstring("Port"), "port: %s", port)
	}
}

// A ${secrets.X} password with no environment to resolve it against must be
// refused before anything is dialled; a managed-credential reference must say so
// plainly rather than failing obscurely.
func TestGetPGVectorOptions_SecretReferences(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	params := goodPGParams()
	params["password"] = "${secrets.PG_PASSWORD}"
	body, code := getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("environment"))

	params["password"] = "${credentials.MY_PG}"
	body, code = getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Managed credentials"))
}

// The columns endpoint needs a table, and its ?kind= is a closed set.
func TestGetPGVectorColumns_TableAndKind(t *testing.T) {
	g := NewWithT(t)
	r := setupPGVectorRouter(&Service{})

	params := goodPGParams()
	params["kind"] = "vector"
	body, code := getPGVectorOptions(r, "columns", params) // no table
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Table"))

	// An unknown kind is refused before any connection is opened.
	params["table"] = "documents_1024"
	params["kind"] = "'; DROP TABLE users; --"
	body, code = getPGVectorOptions(r, "columns", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"]).To(ContainSubstring("Unknown column kind"))

	// The four kinds the markers use are the four kinds that exist.
	g.Expect(pgvectorColumnKinds).To(HaveLen(4))
	g.Expect(pgvectorColumnKinds["vector"]).To(Equal([]string{"vector"}))
	g.Expect(pgvectorColumnKinds["jsonb"]).To(Equal([]string{"jsonb", "json"}))
	g.Expect(pgvectorColumnKinds["any"]).To(BeEmpty()) // empty text[] → no filter
}

func TestPGVectorColumnLabel(t *testing.T) {
	g := NewWithT(t)
	// The dimension is the point: it is what has to match the embedding model.
	g.Expect(pgvectorColumnLabel("embedding", "vector(1024)", "vector")).To(Equal("embedding (vector, 1024 dimensions)"))
	g.Expect(pgvectorColumnLabel("embedding", "vector(1536)", "vector")).To(Equal("embedding (vector, 1536 dimensions)"))
	g.Expect(pgvectorColumnLabel("embedding", "vector", "vector")).To(Equal("embedding (vector)")) // undimensioned
	g.Expect(pgvectorColumnLabel("content", "text", "text")).To(Equal("content (text)"))
	g.Expect(pgvectorColumnLabel("metadata", "jsonb", "jsonb")).To(Equal("metadata (jsonb)"))
	g.Expect(pgvectorColumnLabel("id", "uuid", "uuid")).To(Equal("id (uuid)"))
	g.Expect(pgvectorColumnLabel("title", "character varying(255)", "varchar")).To(Equal("title (character varying(255))"))
}

// ---------------------------------------------------------------------------
// 6. The dropdown wiring matches the manifest.
// ---------------------------------------------------------------------------

func TestPGVectorDynamicOptionsWiring(t *testing.T) {
	g := NewWithT(t)
	const prefix = "vectordatabase/pgvector/"

	// All twelve actions get a Schema picker — table_create included: it creates
	// its table inside an EXISTING schema.
	g.Expect(pgvectorAllActions).To(HaveLen(12))
	for _, a := range pgvectorAllActions {
		marker, ok := dynamicOptionsMetadata[prefix+a+"#schema"]
		g.Expect(ok).To(BeTrue(), "action %s must have a Schema picker", a)
		g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/pgvector-schemas"), "action: %s", a)
		g.Expect(marker.Params).To(Equal(pgConnParams), "action: %s", a)
	}

	// table_create must NOT get a Table picker — the table does not exist yet.
	_, ok := dynamicOptionsMetadata[prefix+"table_create#table"]
	g.Expect(ok).To(BeFalse(), "table_create's Table must stay free-text: the table does not exist yet")

	// …nor a column picker, for the same reason: its column inputs NAME columns
	// about to be created, they do not select existing ones.
	for input := range pgvectorColumnInputs {
		_, ok := dynamicOptionsMetadata[prefix+"table_create#"+input]
		g.Expect(ok).To(BeFalse(), "table_create's %s must stay free-text: the column does not exist yet", input)
	}

	// Every other action that addresses an existing table gets a Table picker.
	g.Expect(pgvectorTableActions).To(HaveLen(11))
	for _, a := range pgvectorTableActions {
		marker, ok := dynamicOptionsMetadata[prefix+a+"#table"]
		g.Expect(ok).To(BeTrue(), "action %s must have a Table picker", a)
		g.Expect(marker.Endpoint).To(Equal("/api/v1/action/options/pgvector-tables"), "action: %s", a)
		g.Expect(marker.Params).To(ContainElement("schema"), "action: %s — the table list is schema-scoped", a)
	}

	// The column pickers carry their kind in the endpoint, and forward enough to
	// identify the table.
	wantKind := map[string]string{
		"vector_column":   "kind=vector",
		"content_column":  "kind=text",
		"metadata_column": "kind=jsonb",
		"id_column":       "kind=any",
	}
	for action, inputs := range pgvectorColumnActions {
		for _, input := range inputs {
			marker, ok := dynamicOptionsMetadata[prefix+action+"#"+input]
			g.Expect(ok).To(BeTrue(), "%s must have a %s picker", action, input)
			g.Expect(marker.Endpoint).To(HavePrefix("/api/v1/action/options/pgvector-columns?"), "%s#%s", action, input)
			g.Expect(marker.Endpoint).To(HaveSuffix(wantKind[input]), "%s#%s", action, input)
			g.Expect(marker.Params).To(ContainElement("table"), "%s#%s", action, input)
			g.Expect(marker.Params).To(ContainElement("schema"), "%s#%s", action, input)
		}
	}

	// A picker must never be wired to an input its action does not have. Per the
	// manifest, document_count has only metadata_column, and index_create only
	// vector_column — no id/content pickers on either.
	for _, absent := range []string{
		"document_count#id_column",
		"document_count#content_column",
		"document_count#vector_column",
		"index_create#id_column",
		"index_create#content_column",
		"index_create#metadata_column",
		"table_info#vector_column",
		"table_info#id_column",
	} {
		_, ok := dynamicOptionsMetadata[prefix+absent]
		g.Expect(ok).To(BeFalse(), "%s: that action has no such input in the manifest", absent)
	}

	// Every forwarded connection param is a real input name on the node.
	g.Expect(pgConnParams).To(Equal([]string{"host", "port", "database", "username", "password", "ssl_mode"}))
}

// ---------------------------------------------------------------------------
// Live integration — skipped unless PGVECTOR_HOST is set, mirroring the
// executor's live-test convention (and the Jenkins proxy's).
//
//	PGVECTOR_HOST=192.168.80.20 PGVECTOR_DB=vectordb PGVECTOR_USER=flomation \
//	PGVECTOR_PASSWORD=… go test ./internal/http/ -run PGVectorLive -v
//
// ---------------------------------------------------------------------------

func livePGParams(t *testing.T) map[string]string {
	host := os.Getenv("PGVECTOR_HOST")
	if host == "" {
		t.Skip("PGVECTOR_HOST not set; skipping live pgvector integration test")
	}
	port := os.Getenv("PGVECTOR_PORT")
	if port == "" {
		port = "5432"
	}
	return map[string]string{
		"host":     host,
		"port":     port,
		"database": os.Getenv("PGVECTOR_DB"),
		"username": os.Getenv("PGVECTOR_USER"),
		"password": os.Getenv("PGVECTOR_PASSWORD"),
		"ssl_mode": "disable",
	}
}

func liveOptionNames(body map[string]any) []string {
	raw, _ := body["options"].([]any)
	names := make([]string, 0, len(raw))
	for _, o := range raw {
		names = append(names, o.(map[string]any)["name"].(string))
	}
	return names
}

func TestPGVectorLive_SchemasTablesColumns(t *testing.T) {
	g := NewWithT(t)
	params := livePGParams(t)
	r := setupPGVectorRouter(&Service{})

	// Schemas.
	body, code := getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(Not(HaveKey("error")), "body: %v", body)
	g.Expect(liveOptionNames(body)).To(ContainElement("public"))
	t.Logf("live schemas: %v", liveOptionNames(body))

	// Tables — only the ones that actually have a vector column.
	body, code = getPGVectorOptions(r, "tables", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(Not(HaveKey("error")), "body: %v", body)
	tables := liveOptionNames(body)
	g.Expect(tables).To(ContainElement("documents_1024"))
	g.Expect(tables).To(ContainElement("documents_1536"))
	t.Logf("live vector tables: %v", tables)

	// Columns, per kind. The vector column's label must carry its dimension —
	// that is the whole reason format_type is in the query.
	params["table"] = "documents_1024"
	for kind, want := range map[string]string{
		"vector": "embedding (vector, 1024 dimensions)",
		"text":   "content (text)",
		"jsonb":  "metadata (jsonb)",
	} {
		p := map[string]string{"kind": kind}
		for k, v := range params {
			p[k] = v
		}
		body, code = getPGVectorOptions(r, "columns", p)
		g.Expect(code).To(Equal(http.StatusOK), "kind: %s", kind)
		g.Expect(body).To(Not(HaveKey("error")), "kind: %s, body: %v", kind, body)
		names := liveOptionNames(body)
		g.Expect(names).To(HaveLen(1), "kind: %s — exactly one column of that kind", kind)
		g.Expect(names).To(ContainElement(want), "kind: %s", kind)
		t.Logf("live columns kind=%s: %v", kind, names)
	}

	// kind=any lists everything, dimension included.
	p := map[string]string{"kind": "any"}
	for k, v := range params {
		p[k] = v
	}
	body, code = getPGVectorOptions(r, "columns", p)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(Not(HaveKey("error")), "body: %v", body)
	g.Expect(liveOptionNames(body)).To(ConsistOf(
		"id (uuid)", "content (text)", "metadata (jsonb)", "embedding (vector, 1024 dimensions)",
	))
	t.Logf("live columns kind=any: %v", liveOptionNames(body))
}

// The 1536-dimension table must be labelled 1536 — the label is what stops an
// operator wiring a 1024-dim model to a 1536-dim column, which Postgres refuses
// at query time with an error that means nothing to a front-of-house user.
func TestPGVectorLive_DimensionInLabel(t *testing.T) {
	g := NewWithT(t)
	params := livePGParams(t)
	params["table"] = "documents_1536"
	params["kind"] = "vector"

	r := setupPGVectorRouter(&Service{})
	body, code := getPGVectorOptions(r, "columns", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(Not(HaveKey("error")), "body: %v", body)
	g.Expect(liveOptionNames(body)).To(ConsistOf("embedding (vector, 1536 dimensions)"))
}

// Wrong credentials against the real server: the operator gets a sentence, and
// the raw pq error (`pq: password authentication failed for user "flomation"`)
// never reaches the client.
func TestPGVectorLive_BadPasswordIsGeneric(t *testing.T) {
	g := NewWithT(t)
	params := livePGParams(t)
	params["password"] = "definitely-not-the-password"

	r := setupPGVectorRouter(&Service{})
	body, code := getPGVectorOptions(r, "schemas", params)
	g.Expect(code).To(Equal(http.StatusOK))
	g.Expect(body).To(HaveKey("error"))
	msg := body["error"].(string)
	g.Expect(msg).To(ContainSubstring("rejected the credentials"))
	g.Expect(msg).To(Not(ContainSubstring("pq:")), "the raw driver error must never reach the client")
	g.Expect(msg).To(Not(ContainSubstring(params["username"])), "the error must not confirm the username back")
	t.Logf("live bad-password message: %q", msg)
}
