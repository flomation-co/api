package http

// Live dropdowns for the Vector Database ▸ pgvector actions: the Schema, Table,
// and the four column pickers (Embedding / Content / Metadata / ID).
//
// ─────────────────────────────────────────────────────────────────────────────
// WHY THIS FILE IS NOT LIKE THE OTHER OPTION PROXIES
// ─────────────────────────────────────────────────────────────────────────────
//
// Every other proxy in this package speaks HTTP to a provider. This one opens a
// raw Postgres connection to a host the caller names, which is a new class of
// egress from the control plane. The api holds every tenant's secrets, so it is
// a different trust boundary from the executor — where reaching an arbitrary
// database host IS the job, and where there is deliberately no allowlist (see
// executor/actions/vectordatabase/pgvector/common.go OpenConn).
//
// The hazard that has no analogue in an HTTP proxy: libpq (and therefore lib/pq)
// falls back to its own PG* environment variables and to host=localhost
// port=5432 when the host is absent. An empty or omitted `host` parameter would
// silently point this proxy at the api's OWN control-plane Postgres over
// loopback and list the platform's internal schema back to any logged-in user.
// An empty host is therefore rejected outright, before a DSN is ever built, and
// so is any host containing '/' (which libpq reads as a unix-socket directory).
//
// The controls, in the order they run:
//
//  1. Host must be non-empty, must not contain '/', and must match
//     ^[A-Za-z0-9._:\[\]-]+$ — which excludes the characters ('/', '@', ' ',
//     '?', ',', '=') that let a crafted host smuggle extra libpq connection
//     parameters.
//  2. The DSN is assembled with net/url, never fmt.Sprintf, so a password
//     containing '@' or '/' cannot restructure it. sslmode is pinned to libpq's
//     six legal values; a raw `options=` parameter is never accepted.
//  3. The dial is gated by a Control hook plumbed through pq.NewConnector +
//     sql.OpenDB (lib/pq has no http.Transport.DialContext analogue — the
//     connector's Dialer is the only seam). It refuses link-local (169.254/16,
//     which is where 169.254.169.254 lives) and the cloud-metadata addresses
//     outside that range. Loopback and RFC1918 stay ALLOWED, exactly as for the
//     Jenkins, Kubernetes and WordPress proxies: customers self-host their
//     database on a LAN, and that is the whole point of the node.
//  4. Only fixed catalog queries run — information_schema and pg_catalog, with
//     every user value ($1 schema, $2 table, $3 column kinds) bound. Schema and
//     table are bound as VALUES in a WHERE clause; they are never interpolated
//     as identifiers, and no user-supplied SQL is executed. Every list is
//     LIMIT 500.
//  5. Bounds: connect_timeout=5 in the DSN, a 10s context on the query,
//     MaxOpenConns(1), MaxIdleConns(0), and the *sql.DB is closed on every path.
//     There is deliberately NO connection cache — an *sql.DB cache would pin
//     tenants' database credentials in api memory for the process lifetime.
//  6. pq errors carry server internals ("pq: password authentication failed for
//     user \"x\""). They are logged raw and never returned to the client.
//
// As with every option proxy the response is ALWAYS HTTP 200: {"options": [...]}
// on success, {"error": "..."} on failure, so the editor renders the message
// inline and falls back to free-text entry.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	gohttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// Dropdown registration
// ---------------------------------------------------------------------------

// pgConnParams are the connection inputs the editor forwards on every pgvector
// option fetch. `environment` is not listed: the editor appends it to any marker
// that declares params, and it is what lets the ${secrets.X} password be
// resolved server-side.
var pgConnParams = []string{"host", "port", "database", "username", "password", "ssl_mode"}

// The table picker additionally needs the chosen schema; the column pickers need
// the schema and the table.
var (
	pgSchemaScopedParams = append(append([]string{}, pgConnParams...), "schema")
	pgTableScopedParams  = append(append([]string{}, pgConnParams...), "schema", "table")
)

// pgvectorTableActions are the actions whose Table input addresses an EXISTING
// table, so it can be a dropdown. table_create is deliberately absent: the table
// it names does not exist yet.
var pgvectorTableActions = []string{
	"document_count", "document_delete", "document_get", "document_insert",
	"document_list", "document_search", "document_update", "document_upsert",
	"hybrid_search", "index_create", "table_info",
}

// pgvectorColumnInputs maps a column input to the kind filter its picker uses.
// One endpoint serves all four; the kind rides in the endpoint's query string.
var pgvectorColumnInputs = map[string]string{
	"vector_column":   "vector",
	"content_column":  "text",
	"metadata_column": "jsonb",
	"id_column":       "any",
}

// pgvectorColumnActions lists, per action, the column inputs that action actually
// has — read off the executor manifest. An input an action does not declare must
// not get a marker.
//
// table_create is absent for the same reason it has no Table picker: its
// id/content/metadata/vector column inputs NAME columns that are about to be
// created, they do not select existing ones. A picker there could only ever list
// the columns of a table that does not exist, so it would show an error and the
// operator would type the name anyway.
var pgvectorColumnActions = map[string][]string{
	"document_count":  {"metadata_column"},
	"document_delete": {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_get":    {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_insert": {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_list":   {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_search": {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_update": {"id_column", "content_column", "metadata_column", "vector_column"},
	"document_upsert": {"id_column", "content_column", "metadata_column", "vector_column"},
	"hybrid_search":   {"id_column", "content_column", "metadata_column", "vector_column"},
	"index_create":    {"vector_column"},
}

// pgvectorAllActions is every pgvector action. All twelve get a Schema picker —
// including table_create, which creates its table INSIDE an existing schema.
var pgvectorAllActions = []string{
	"document_count", "document_delete", "document_get", "document_insert",
	"document_list", "document_search", "document_update", "document_upsert",
	"hybrid_search", "index_create", "table_create", "table_info",
}

// init registers the pgvector live dropdowns into the shared
// dynamicOptionsMetadata map (declared in action.go). They are registered from a
// table here rather than spelled out as ~50 literal entries in action.go, which
// is the same approach kubernetes_options.go takes for its ~120: the pattern is
// entirely regular, and the table is checkable against the manifest at a glance.
// Package-level variables are initialised before any init() runs, so
// dynamicOptionsMetadata is non-nil at this point.
func init() {
	register := func(actionID, input, endpoint string, params []string) {
		dynamicOptionsMetadata["vectordatabase/pgvector/"+actionID+"#"+input] = api.InputDynamicOptions{
			Endpoint: "/api/v1/action/options/" + endpoint,
			Params:   params,
		}
	}

	for _, a := range pgvectorAllActions {
		register(a, "schema", "pgvector-schemas", pgConnParams)
	}
	for _, a := range pgvectorTableActions {
		register(a, "table", "pgvector-tables", pgSchemaScopedParams)
	}
	for action, inputs := range pgvectorColumnActions {
		for _, input := range inputs {
			kind, known := pgvectorColumnInputs[input]
			if !known {
				panic("pgvector options: unknown column input " + input)
			}
			register(action, input, "pgvector-columns?kind="+kind, pgTableScopedParams)
		}
	}
}

// ---------------------------------------------------------------------------
// Connection resolution + hardening
// ---------------------------------------------------------------------------

const (
	// pgOptionListLimit bounds every dropdown. A database with more schemas,
	// vector tables or columns than this is not usefully browsed from a select
	// box; the operator types the name (the editor always allows free text). It
	// is a string because it is concatenated into the catalog query constants
	// below (it is never a user value, so there is no injection surface).
	pgOptionListLimit = "500"

	// pgConnectTimeoutSeconds bounds the TCP dial + startup handshake. Without it
	// a black-holed host hangs until the OS TCP timeout (~2 minutes) with the
	// editor spinning on the other end.
	pgConnectTimeoutSeconds = 5

	// pgQueryTimeout bounds the whole catalog read, dial included. lib/pq turns a
	// context deadline into a real Postgres CancelRequest, so this kills the
	// server-side query rather than merely abandoning the caller.
	pgQueryTimeout = 10 * time.Second
)

// pgHostPattern is the set of characters a Postgres host may contain: letters,
// digits, dot, dash, underscore-free — plus ':' and '[' ']' for IPv6 literals.
//
// What it EXCLUDES is the point. '/' would make libpq read the value as a
// unix-socket directory (and reach the api's own socket). ' ', '=', ',' and '?'
// are the separators that would let a crafted host smuggle further libpq
// connection parameters ("evil host=127.0.0.1", "h?options=-c") into the DSN.
// '@' would restructure the URL's userinfo. None of them can appear in a real
// hostname or IP.
var pgHostPattern = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]+$`)

// pgValidSSLModes is libpq's complete, closed set. Anything else is refused
// rather than passed through, so no unknown keyword ever reaches the connection
// string. Mirrors validSSLModes in the executor's pgvector package.
var pgValidSSLModes = map[string]struct{}{
	"disable": {}, "allow": {}, "prefer": {},
	"require": {}, "verify-ca": {}, "verify-full": {},
}

// pgProxyConn is one validated connection, ready to be turned into a DSN.
type pgProxyConn struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
	SSLMode  string
}

// validatePGVectorHost is the control that stops the proxy pointing at the api's
// own control-plane database. It returns the normalised host (IPv6 literals get
// their brackets stripped, because net.JoinHostPort puts them back) or an
// operator-facing message.
func validatePGVectorHost(host string) (string, string) {
	host = strings.TrimSpace(host)

	// The load-bearing check. Empty means libpq would fall back to its PG*
	// environment and to host=localhost — i.e. the api's own Postgres.
	if host == "" {
		return "", "Set the Database Host to load this list"
	}
	// An unresolved ${var}/${secrets.X} reference: the editor could not resolve
	// it, and it is not a host.
	if strings.HasPrefix(host, "${") {
		return "", "The Database Host is a variable that can't be resolved here — type the host to load this list"
	}
	// A '/' would be read by libpq as a unix-socket directory. Checked explicitly
	// (the pattern below rejects it too) so the operator gets a specific message.
	if strings.Contains(host, "/") {
		return "", "The Database Host must be a hostname or IP address — no scheme, path, or socket directory"
	}
	if !pgHostPattern.MatchString(host) {
		return "", "The Database Host must be a hostname or IP address — no scheme, port, spaces, or connection parameters"
	}

	// Strip the brackets of an IPv6 literal; net.JoinHostPort re-adds them, and
	// would otherwise produce "[[::1]]:5432".
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	// A remaining ':' in something that is not an IP means the operator typed
	// "host:5432". JoinHostPort would bracket it into nonsense, so say so.
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", "The Database Host must not include a port — set the Port field instead"
	}
	return host, ""
}

// resolvePGVectorConn reads and validates the connection out of the query
// parameters, resolving the password secret server-side. A non-empty message is
// operator-facing text to render in place of the dropdown; the caller must stop.
// An empty message with ok==false means the response was already written.
func (s *Service) resolvePGVectorConn(c *gin.Context) (pgProxyConn, string, bool) {
	host, errMsg := validatePGVectorHost(c.Query("host"))
	if errMsg != "" {
		return pgProxyConn{}, errMsg, false
	}

	database := strings.TrimSpace(c.Query("database"))
	if database == "" || strings.HasPrefix(database, "${") {
		return pgProxyConn{}, "Set the Database to load this list", false
	}

	username := strings.TrimSpace(c.Query("username"))
	if username == "" || strings.HasPrefix(username, "${") {
		return pgProxyConn{}, "Set the Username to load this list", false
	}

	// Port, schema and ssl_mode default exactly as the executor's GetAuth does, so
	// the dropdown reads the same database the flow will.
	port := 5432
	if raw := strings.TrimSpace(c.Query("port")); raw != "" && !strings.HasPrefix(raw, "${") {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			return pgProxyConn{}, "The Port must be a number between 1 and 65535", false
		}
		port = n
	}

	sslMode := strings.TrimSpace(c.Query("ssl_mode"))
	if sslMode == "" || strings.HasPrefix(sslMode, "${") {
		sslMode = "disable"
	}
	if _, ok := pgValidSSLModes[sslMode]; !ok {
		return pgProxyConn{}, "The SSL Mode is not one of Postgres' supported values", false
	}

	password := strings.TrimSpace(c.Query("password"))
	if strings.HasPrefix(password, "${credentials.") || strings.HasPrefix(password, "${credential.") {
		return pgProxyConn{}, "Managed credentials can't be used to load this list — use an environment secret for the password (the flow itself still runs)", false
	}
	if strings.HasPrefix(password, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			return pgProxyConn{}, "Select an environment to resolve the database password", false
		}
		// Resolving a secret to plaintext here must be gated by the same permission
		// as reading it through the environment endpoints: the resolved value
		// authenticates a connection to a caller-supplied host, so without this
		// check a member denied environment.view could exfiltrate any secret by
		// aiming the proxy at a database they control.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return pgProxyConn{}, "", false // checkPermission has written the response
		}
		resolved, msg := s.resolveEnvironmentSecret(c, environmentID, password)
		if msg != "" {
			return pgProxyConn{}, msg, false
		}
		password = resolved
	}
	if password == "" {
		return pgProxyConn{}, "Set the Password to load this list", false
	}

	return pgProxyConn{
		Host:     host,
		Port:     port,
		Database: database,
		Username: username,
		Password: password,
		SSLMode:  sslMode,
	}, "", true
}

// dsn builds the libpq connection URL with net/url rather than fmt.Sprintf, so a
// password containing '@' or '/' cannot restructure the URL and the host (already
// pattern-checked) cannot introduce extra keywords. Only sslmode and
// connect_timeout are ever set — notably never `options`, which can carry
// arbitrary server-side GUCs (options=-c search_path=…).
func (conn pgProxyConn) dsn() string {
	q := url.Values{}
	q.Set("sslmode", conn.SSLMode)
	q.Set("connect_timeout", strconv.Itoa(pgConnectTimeoutSeconds))
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(conn.Username, conn.Password),
		Host:     net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port)),
		Path:     "/" + conn.Database,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// pgvectorDialControl refuses link-local and cloud-metadata destinations. It runs
// on the address actually dialled, so a DNS name resolving to one of them is
// caught too. Loopback and private LAN ranges stay allowed — a self-hosted
// Postgres almost always lives there, exactly as with the Jenkins and Kubernetes
// proxies. isCloudMetadataIP is shared with them (jenkins_options.go).
func pgvectorDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		// Covers 169.254.169.254 and its ::ffff: mapped form.
		return errors.New("link-local addresses are not allowed")
	}
	if isCloudMetadataIP(ip) {
		return errors.New("cloud metadata addresses are not allowed")
	}
	return nil
}

// pgvectorDialer adapts a Control-hooked net.Dialer to lib/pq. pq takes a
// Dialer (Dial + DialTimeout) and prefers a DialerContext (DialContext) when the
// value implements it; this implements all three, so the Control hook runs
// whichever path pq takes. This is the ONLY seam for an SSRF guard on a Postgres
// connection — there is no http.Transport.DialContext analogue.
type pgvectorDialer struct{ d *net.Dialer }

func newPGVectorDialer() pgvectorDialer {
	return pgvectorDialer{d: &net.Dialer{
		Timeout: pgConnectTimeoutSeconds * time.Second,
		Control: pgvectorDialControl,
	}}
}

func (p pgvectorDialer) Dial(network, address string) (net.Conn, error) {
	return p.d.Dial(network, address)
}

func (p pgvectorDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.d.DialContext(ctx, network, address)
}

func (p pgvectorDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.d.DialContext(ctx, network, address)
}

// openPGVectorDB returns a live, bounded, SSRF-guarded connection.
//
// The connector route (pq.NewConnector + sql.OpenDB) exists purely so the dialer
// can be replaced; sql.Open("postgres", dsn) gives no way to reach the dial. The
// caller MUST Close the returned handle: there is no cache, by design. Caching an
// *sql.DB would keep a tenant's database password live in api memory for the
// lifetime of the process, which is not a trade worth making to save a dial on a
// dropdown.
func openPGVectorDB(conn pgProxyConn) (*sql.DB, error) {
	connector, err := pq.NewConnector(conn.dsn())
	if err != nil {
		return nil, err
	}
	connector.Dialer(newPGVectorDialer())

	db := sql.OpenDB(connector)
	// One connection, none idle: a dropdown fetch runs exactly one query, and
	// nothing here should outlive the request.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(pgQueryTimeout)
	return db, nil
}

// pgvectorQuery runs one fixed catalog query and maps each row to an option. It
// owns the whole lifecycle — open, bounded query, close — so no caller can leak a
// connection, and it is the single place the raw driver error is swallowed.
//
// The error returned to the client is deliberately generic. A pq error carries
// server internals: `pq: password authentication failed for user "flomation"`
// confirms a username, and `pq: database "x" does not exist` enumerates
// databases. The raw error is logged; the operator gets a sentence.
func pgvectorQuery(
	c *gin.Context,
	conn pgProxyConn,
	what string,
	query string,
	args []any,
	scan func(*sql.Rows) (api.InputOption, bool, error),
) ([]api.InputOption, string) {
	db, err := openPGVectorDB(conn)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "list": what}).Warn("pgvector options: could not build the connection")
		return nil, "Could not connect to the database — check the Host, Port, Database and SSL Mode"
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(c.Request.Context(), pgQueryTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		// The one place a caller-supplied host's server error reaches us.
		log.WithFields(log.Fields{"error": err, "list": what, "host": conn.Host}).
			Warn("pgvector options: catalog query failed")
		return nil, pgvectorErrorMessage(err, what)
	}
	defer func() { _ = rows.Close() }()

	options := make([]api.InputOption, 0, 16)
	for rows.Next() {
		opt, keep, err := scan(rows)
		if err != nil {
			log.WithFields(log.Fields{"error": err, "list": what}).Warn("pgvector options: could not read a catalog row")
			return nil, fmt.Sprintf("Could not read the %s from the database", what)
		}
		if keep {
			options = append(options, opt)
		}
	}
	if err := rows.Err(); err != nil {
		log.WithFields(log.Fields{"error": err, "list": what}).Warn("pgvector options: catalog row scan failed")
		return nil, fmt.Sprintf("Could not read the %s from the database", what)
	}
	return options, ""
}

// pgvectorErrorMessage maps a driver failure to operator-facing text WITHOUT
// echoing the driver's own words. Only the coarse class of failure is disclosed:
// the classes below are ones the operator can act on, and none of them reveal
// anything the operator did not already type.
func pgvectorErrorMessage(err error, what string) string {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code.Class() {
		case "28": // invalid_authorization_specification / invalid_password
			return "The database rejected the credentials — check the Username and Password"
		case "3D": // invalid_catalog_name
			return "That Database does not exist on the server — check the Database name"
		case "42": // syntax_error_or_access_rule_violation — here, always a privilege
			return fmt.Sprintf("The database user is not allowed to read the %s — grant it catalog access, or type the name manually", what)
		}
		return fmt.Sprintf("The database returned an error while listing the %s", what)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The database took too long to answer — type the name manually (the flow itself still runs)"
	}
	return "Could not reach the database — check the Host, Port and SSL Mode, and that the server is running"
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// pgvectorSchemasQuery lists the schemas the connected user can actually use.
//
// pg_catalog.pg_namespace, not information_schema.schemata: the latter only shows
// schemas the current role OWNS, so a least-privilege read-only database user —
// the right user to point a vector search at — would see an empty dropdown.
// has_schema_privilege(…, 'USAGE') is the question actually worth asking.
const pgvectorSchemasQuery = `
	SELECT n.nspname
	  FROM pg_catalog.pg_namespace n
	 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
	   AND left(n.nspname, 3) <> 'pg_'
	   AND has_schema_privilege(n.oid, 'USAGE')
	 ORDER BY 1
	 LIMIT ` + pgOptionListLimit

// getPGVectorSchemas serves the database's schemas for the Schema input.
func (s *Service) getPGVectorSchemas(c *gin.Context) {
	conn, errMsg, ok := s.resolvePGVectorConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	options, errMsg := pgvectorQuery(c, conn, "schemas", pgvectorSchemasQuery, nil,
		func(rows *sql.Rows) (api.InputOption, bool, error) {
			var name string
			if err := rows.Scan(&name); err != nil {
				return api.InputOption{}, false, err
			}
			return api.InputOption{Name: name, Value: name}, name != "", nil
		})
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// pgvectorTablesQuery lists only the tables that actually HAVE a vector column.
//
// That is what makes this dropdown worth having: pointed at a real application
// database, a plain table list is hundreds of rows of irrelevance, and the two
// the operator wants are the ones pgvector can search. The join to
// information_schema.tables restricts it to BASE TABLEs (a view over a vector
// column cannot be INSERTed into), and information_schema is privilege-filtered
// by Postgres, so the list never names a table the user cannot see.
//
// $1 is the schema, bound as a VALUE in the WHERE clause — never interpolated as
// an identifier. Empty means "every schema", which is how an operator discovers
// where their vector tables live.
const pgvectorTablesQuery = `
	SELECT c.table_schema, c.table_name
	  FROM information_schema.columns c
	  JOIN information_schema.tables t
	    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
	 WHERE c.udt_name = 'vector'
	   AND t.table_type = 'BASE TABLE'
	   AND c.table_schema NOT IN ('pg_catalog', 'information_schema')
	   AND ($1 = '' OR c.table_schema = $1)
	 ORDER BY 1, 2
	 LIMIT ` + pgOptionListLimit

// getPGVectorTables serves the vector-bearing tables for the Table input.
func (s *Service) getPGVectorTables(c *gin.Context) {
	conn, errMsg, ok := s.resolvePGVectorConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	// An unresolved ${var} is not a schema; treat it as "not chosen" rather than
	// binding the literal text and returning nothing.
	schema := strings.TrimSpace(c.Query("schema"))
	if strings.HasPrefix(schema, "${") {
		schema = ""
	}

	// The Table input holds a bare table name; the schema is a separate input. So
	// when a row comes from a schema other than the one in effect, the option is
	// LABELLED "other_schema.table" — the operator can see they must set Schema to
	// match — while the value stays the bare name the input expects.
	effective := schema
	if effective == "" {
		effective = "public" // the executor's default when Schema is blank
	}

	options, errMsg := pgvectorQuery(c, conn, "tables", pgvectorTablesQuery, []any{schema},
		func(rows *sql.Rows) (api.InputOption, bool, error) {
			var tableSchema, tableName string
			if err := rows.Scan(&tableSchema, &tableName); err != nil {
				return api.InputOption{}, false, err
			}
			if tableName == "" {
				return api.InputOption{}, false, nil
			}
			label := tableName
			if tableSchema != effective {
				label = tableSchema + "." + tableName
			}
			return api.InputOption{Name: label, Value: tableName}, true, nil
		})
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// pgvectorColumnKinds maps the ?kind= query parameter to the Postgres type names
// that satisfy it. One endpoint serves all four column dropdowns.
//
// The kinds are matched on pg_type.typname (the same value information_schema
// exposes as udt_name), so `varchar`/`bpchar` are the internal spellings of
// `character varying`/`character`. citext is included because a
// case-insensitive content column is a perfectly ordinary choice.
//
// An empty slice means "no type filter" — see the query: it is bound as a
// text[], so even this is a BIND VALUE and not a fragment of SQL text.
var pgvectorColumnKinds = map[string][]string{
	"vector": {"vector"},
	"text":   {"text", "varchar", "bpchar", "citext"},
	"jsonb":  {"jsonb", "json"},
	"any":    {},
}

// pgvectorColumnsQuery lists one table's columns.
//
// pg_catalog rather than information_schema.columns because of format_type: it is
// the only way to recover a vector column's DIMENSION (vector(1024)), and the
// dimension is the single most useful thing to show an operator — it is what has
// to match their embedding model, and Postgres refuses, hard, to compare vectors
// of different lengths.
//
// Every user value is bound: $1 schema, $2 table, $3 the kind's type names as a
// text[]. cardinality($3) = 0 is the "any kind" case. relkind covers ordinary and
// partitioned tables plus views and foreign tables, which are legitimate read
// targets. has_column_privilege keeps the list to columns the user can SELECT.
const pgvectorColumnsQuery = `
	SELECT a.attname,
	       format_type(a.atttypid, a.atttypmod),
	       t.typname
	  FROM pg_catalog.pg_attribute  a
	  JOIN pg_catalog.pg_class      c ON c.oid = a.attrelid
	  JOIN pg_catalog.pg_namespace  n ON n.oid = c.relnamespace
	  JOIN pg_catalog.pg_type       t ON t.oid = a.atttypid
	 WHERE n.nspname = $1
	   AND c.relname = $2
	   AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
	   AND a.attnum > 0
	   AND NOT a.attisdropped
	   AND has_column_privilege(c.oid, a.attnum, 'SELECT')
	   AND (cardinality($3::text[]) = 0 OR t.typname = ANY($3::text[]))
	 ORDER BY a.attnum
	 LIMIT ` + pgOptionListLimit

// pgVectorDimsPattern pulls the dimension out of format_type's "vector(1024)".
var pgVectorDimsPattern = regexp.MustCompile(`^vector\((\d+)\)$`)

// pgvectorColumnLabel is what the operator reads in the dropdown. The type is
// part of the label on purpose — "embedding (vector, 1024 dimensions)" tells them
// at a glance which of documents_1024 / documents_1536 they are pointed at, which
// is exactly the mistake the label exists to prevent.
func pgvectorColumnLabel(name, formatted, typname string) string {
	if typname == "vector" {
		if m := pgVectorDimsPattern.FindStringSubmatch(formatted); m != nil {
			return fmt.Sprintf("%s (vector, %s dimensions)", name, m[1])
		}
		return name + " (vector)"
	}
	if formatted == "" {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, formatted)
}

// getPGVectorColumns serves one table's columns, filtered by ?kind=, for the
// Embedding / Content / Metadata / ID column inputs.
func (s *Service) getPGVectorColumns(c *gin.Context) {
	// The kind is OUR parameter, not the operator's — it is baked into the
	// endpoint URL in the marker. Anything not in the closed set is a bug or a
	// hand-crafted request; either way it must not reach the query.
	kind := strings.TrimSpace(c.Query("kind"))
	if kind == "" {
		kind = "any"
	}
	types, known := pgvectorColumnKinds[kind]
	if !known {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Unknown column kind"})
		return
	}

	conn, errMsg, ok := s.resolvePGVectorConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	table := strings.TrimSpace(c.Query("table"))
	if table == "" || strings.HasPrefix(table, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Table to load its columns"})
		return
	}

	schema := strings.TrimSpace(c.Query("schema"))
	if schema == "" || strings.HasPrefix(schema, "${") {
		schema = "public" // the executor's default when Schema is blank
	}

	options, errMsg := pgvectorQuery(c, conn, "columns", pgvectorColumnsQuery,
		[]any{schema, table, pq.Array(types)},
		func(rows *sql.Rows) (api.InputOption, bool, error) {
			var name, formatted, typname string
			if err := rows.Scan(&name, &formatted, &typname); err != nil {
				return api.InputOption{}, false, err
			}
			if name == "" {
				return api.InputOption{}, false, nil
			}
			return api.InputOption{
				Name:  pgvectorColumnLabel(name, formatted, typname),
				Value: name,
			}, true, nil
		})
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}
