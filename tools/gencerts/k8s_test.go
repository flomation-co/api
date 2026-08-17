package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake API server
// ---------------------------------------------------------------------------

type recordedRequest struct {
	Method string
	Path   string
	Body   []byte
}

type storedSecret struct {
	data            map[string]string // base64, exactly as the wire carries it
	resourceVersion int
}

// fakeAPI is enough of the Secrets endpoint to exercise create/read/replace,
// including the conflict paths that matter for two Jobs racing.
type fakeAPI struct {
	t         *testing.T
	namespace string

	mu       sync.Mutex
	secrets  map[string]*storedSecret
	requests []recordedRequest
	// hideGets makes the first n GETs of a name answer 404 even though the
	// object exists — how a lost create race looks from inside.
	hideGets map[string]int
	// onCommit, if set, runs inside a POST after the object has been stored
	// but before the response is written: the one window where a SIGTERM
	// leaves the API server holding a Secret the client never heard about.
	// It is called with f.mu held, so it must not call back into fakeAPI.
	onCommit func(r *http.Request, name string)

	server *httptest.Server
}

func newFakeAPI(t *testing.T, namespace string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:         t,
		namespace: namespace,
		secrets:   map[string]*storedSecret{},
		hideGets:  map[string]int{},
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	body := readAll(f.t, r)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})

	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "bad or missing bearer token")
		return
	}

	prefix := "/api/v1/namespaces/" + f.namespace + "/secrets"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeStatus(w, http.StatusNotFound, "NotFound", "unexpected path "+r.URL.Path)
		return
	}
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")

	switch r.Method {
	case http.MethodGet:
		f.get(w, name)
	case http.MethodPost:
		f.create(w, r, body)
	case http.MethodPut:
		f.replace(w, name, body)
	default:
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func (f *fakeAPI) get(w http.ResponseWriter, name string) {
	s, ok := f.secrets[name]
	if ok && f.hideGets[name] > 0 {
		f.hideGets[name]--
		ok = false
	}
	if !ok {
		writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("secrets %q not found", name))
		return
	}
	writeSecret(w, http.StatusOK, f.namespace, name, s)
}

func (f *fakeAPI) create(w http.ResponseWriter, r *http.Request, body []byte) {
	var in secret
	if err := json.Unmarshal(body, &in); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if _, exists := f.secrets[in.Metadata.Name]; exists {
		writeStatus(w, http.StatusConflict, "AlreadyExists", fmt.Sprintf("secrets %q already exists", in.Metadata.Name))
		return
	}
	s := &storedSecret{data: in.Data, resourceVersion: 1}
	f.secrets[in.Metadata.Name] = s
	// The object is committed from here on, exactly as etcd would have it,
	// whether or not the client ever reads the response.
	if f.onCommit != nil {
		f.onCommit(r, in.Metadata.Name)
	}
	writeSecret(w, http.StatusCreated, f.namespace, in.Metadata.Name, s)
}

func (f *fakeAPI) replace(w http.ResponseWriter, name string, body []byte) {
	var in secret
	if err := json.Unmarshal(body, &in); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	s, ok := f.secrets[name]
	if !ok {
		writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("secrets %q not found", name))
		return
	}
	if in.Metadata.ResourceVersion != strconv.Itoa(s.resourceVersion) {
		writeStatus(w, http.StatusConflict, "Conflict", "the object has been modified")
		return
	}
	s.data = in.Data
	s.resourceVersion++
	writeSecret(w, http.StatusOK, f.namespace, name, s)
}

// data returns a stored Secret decoded, or fails the test if it is absent.
func (f *fakeAPI) data(name string) map[string][]byte {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	s, ok := f.secrets[name]
	if !ok {
		f.t.Fatalf("secret %s was never created", name)
	}
	out := make(map[string][]byte, len(s.data))
	for k, v := range s.data {
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			f.t.Fatalf("secret %s key %q is not valid base64: %v", name, k, err)
		}
		out[k] = decoded
	}
	return out
}

func (f *fakeAPI) put(name string, data map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	encoded := make(map[string]string, len(data))
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString(v)
	}
	f.secrets[name] = &storedSecret{data: encoded, resourceVersion: 1}
}

func (f *fakeAPI) storedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.secrets)
}

func (f *fakeAPI) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.secrets))
	for n := range f.secrets {
		out = append(out, n)
	}
	return out
}

func (f *fakeAPI) exists(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.secrets[name]
	return ok
}

// setOnCommit installs (or clears) the post-commit hook under the same lock the
// handler reads it with, so swapping it between runs is not a data race.
func (f *fakeAPI) setOnCommit(hook func(r *http.Request, name string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCommit = hook
}

func (f *fakeAPI) drop(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.secrets, name)
}

func (f *fakeAPI) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// countRequests counts calls by method against a Secret name ("" = collection).
func (f *fakeAPI) countRequests(method, name string) int {
	n := 0
	for _, r := range f.recorded() {
		if r.Method != method {
			continue
		}
		if name == "" || strings.HasSuffix(r.Path, "/secrets/"+name) {
			n++
		}
	}
	return n
}

func writeSecret(w http.ResponseWriter, code int, namespace, name string, s *storedSecret) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(secret{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: secretMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: strconv.Itoa(s.resourceVersion),
		},
		Type: "Opaque",
		Data: s.data,
	})
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"message": message, "reason": reason, "code": code,
	})
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf
		}
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// withServiceAccount points the ServiceAccount lookups at a temp directory
// holding a token, the fake API server's certificate as the cluster CA, and a
// namespace — the three files the kubelet projects into a real pod.
func withServiceAccount(t *testing.T, f *fakeAPI, caPEM []byte) {
	t.Helper()
	dir := t.TempDir()

	if caPEM == nil {
		caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})
	}
	write := func(name string, content []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("token", []byte("test-token\n"))
	write("ca.crt", caPEM)
	write("namespace", []byte(f.namespace+"\n"))

	old := saDir
	saDir = dir
	t.Cleanup(func() { saDir = old })
}

func k8sOpts(f *fakeAPI, prefix string) genOpts {
	return genOpts{
		CACN:            "Flomation Test CA",
		CAOrg:           "Flomation",
		CAValidity:      3650 * 24 * time.Hour,
		CertValidity:    365 * 24 * time.Hour,
		Services:        []serviceSpec{{Name: "api", CN: "api"}, {Name: "launch", CN: "launch"}, {Name: "runner", CN: "runner"}},
		K8sSecretPrefix: prefix,
		K8sAPI:          f.server.URL,
		K8sNamespace:    f.namespace,
	}
}

// mintTestCA produces a CA to pre-seed the fake API server with.
func mintTestCA(t *testing.T, cn string) (certPEM, keyPEM []byte, cert *x509.Certificate) {
	t.Helper()
	caCert, caKey, err := generateCA(genOpts{CACN: cn, CAOrg: "Flomation", CAValidity: 3650 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	keyPEM, err = encodeECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("encode CA key: %v", err)
	}
	return encodePEM("CERTIFICATE", caCert.Raw), keyPEM, caCert
}

// mintTestLeaf signs one leaf with a CA held as PEM — a Secret that a previous
// install would have left behind.
func mintTestLeaf(t *testing.T, caCertPEM, caKeyPEM []byte, service string) (certPEM, keyPEM []byte) {
	t.Helper()
	caCert, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("parseCA: %v", err)
	}
	certPEM, keyPEM, err = generateServiceCert(
		genOpts{CAOrg: "Flomation", CertValidity: 365 * 24 * time.Hour},
		serviceSpec{Name: service, CN: service}, caCert, caKey)
	if err != nil {
		t.Fatalf("generateServiceCert: %v", err)
	}
	return certPEM, keyPEM
}

func verifyLeaf(t *testing.T, caPEM, leafPEM []byte) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM did not load into a pool")
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatal("leaf is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf does not verify against the expected CA: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// The CA private key must never reach a per-service Secret. If it did, any
// compromised service could mint an identity for every other service and the
// mesh's trust model would be gone.
func TestCAKeyNeverLeavesTheCASecret(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run: %v", err)
	}

	caData := f.data("flo-ca")
	caKeyPEM := caData[keyCAKey]
	if len(caKeyPEM) == 0 {
		t.Fatal("CA secret holds no ca-key.pem")
	}
	if _, _, err := parseCA(caData[keyCACert], caKeyPEM); err != nil {
		t.Fatalf("CA secret does not hold a usable CA: %v", err)
	}
	if got := sortedKeys(caData); strings.Join(got, ",") != "ca-key.pem,ca.pem" {
		t.Fatalf("CA secret keys = %v, want [ca-key.pem ca.pem]", got)
	}

	caKeyB64 := base64.StdEncoding.EncodeToString(caKeyPEM)
	caKey, err := parseECPrivateKey(caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}

	for _, svc := range []string{"api", "launch", "runner"} {
		name := "flo-" + svc
		data := f.data(name)

		if got := sortedKeys(data); strings.Join(got, ",") != "ca.pem,tls.crt,tls.key" {
			t.Fatalf("%s keys = %v, want [ca.pem tls.crt tls.key]", name, got)
		}
		for k, v := range data {
			if strings.Contains(string(v), string(caKeyPEM)) {
				t.Fatalf("%s key %q contains the CA private key", name, k)
			}
		}
		// The leaf key must be its own key, not the CA's.
		leafKey, err := parseECPrivateKey(data[keyTLSKey])
		if err != nil {
			t.Fatalf("%s: parse tls.key: %v", name, err)
		}
		if leafKey.Equal(caKey) {
			t.Fatalf("%s tls.key IS the CA key", name)
		}
		verifyLeaf(t, data[keyCACert], data[keyTLSCert])
	}

	// Belt and braces: the key must not appear on the wire either, in any
	// request that is not the CA Secret's own create.
	for _, r := range f.recorded() {
		if strings.Contains(string(r.Body), "flo-ca") {
			continue
		}
		if strings.Contains(string(r.Body), caKeyB64) || strings.Contains(string(r.Body), string(caKeyPEM)) {
			t.Fatalf("CA private key found in a %s %s body", r.Method, r.Path)
		}
	}

	// The comparisons above are over PEM text, so a leak that re-encoded the
	// same key — SEC1 to PKCS#8, or bare DER — would walk straight past them.
	// The key itself is what has to stay in flo-ca, so hunt for that,
	// everywhere, whatever it is wrapped in.
	needles := caKeyNeedles(t, caKey)

	for _, name := range f.names() {
		if name == "flo-ca" {
			continue
		}
		for k, v := range f.data(name) {
			if what := findCAKeyMaterial(v, needles); what != "" {
				t.Fatalf("secret %s key %q carries the CA %s", name, k, what)
			}
		}
	}
	for _, r := range f.recorded() {
		if strings.Contains(string(r.Body), `"flo-ca"`) {
			continue
		}
		// Whole body first, then every string in it. Decoding into the `secret`
		// struct and scanning only data[] would check the one field we already
		// expect to be right: anything else the client puts on the wire — an
		// annotation, a label, stringData, a body shape this struct does not
		// model — reaches the API server just the same, and json.Unmarshal into
		// a struct drops it silently. Walking the document also unescapes it,
		// without which a PEM inside a JSON string is a run of literal \n and
		// matches nothing.
		if what := findCAKeyMaterial(r.Body, needles); what != "" {
			t.Fatalf("%s %s body carries the CA %s", r.Method, r.Path, what)
		}
		var doc any
		if json.Unmarshal(r.Body, &doc) != nil {
			continue
		}
		forEachJSONString(doc, func(s string) {
			if what := findCAKeyMaterial([]byte(s), needles); what != "" {
				t.Fatalf("%s %s carries the CA %s in a JSON string", r.Method, r.Path, what)
			}
		})
	}
}

// caKeyNeedle is one encoding of the CA private key to hunt for.
type caKeyNeedle struct {
	what string
	enc  []byte
}

// caKeyNeedles returns every byte sequence that would give the CA private key
// away.
//
// PrivateKey.Bytes is the raw scalar, fixed-width for the curve, which is
// exactly how both DER encodings carry it — so that one needle catches a SEC1
// leak, a PKCS#8 leak and a bare dump alike. It replaces caKey.D.Bytes(),
// deprecated in Go 1.26, and is the better needle besides: big.Int.Bytes
// strips leading zero bytes, so for roughly one P-256 key in 256 it produced a
// 31-byte value that is not what any encoder actually writes.
//
// The two marshalled forms are searched as well. They are redundant — each
// contains the scalar — but they cost nothing and make a failure name the
// encoding it found, which is the first thing anyone chasing a leak wants.
func caKeyNeedles(t *testing.T, caKey *ecdsa.PrivateKey) []caKeyNeedle {
	t.Helper()

	scalar, err := caKey.Bytes()
	if err != nil {
		t.Fatalf("CA key raw scalar: %v", err)
	}
	sec1DER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("marshal CA key as SEC1: %v", err)
	}
	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatalf("marshal CA key as PKCS#8: %v", err)
	}
	return []caKeyNeedle{
		{"private scalar", scalar},
		{"private key in SEC1 DER", sec1DER},
		{"private key in PKCS#8 DER", pkcs8DER},
	}
}

// findCAKeyMaterial names the encoding of the CA private key hiding in blob, or
// returns "" if there is none.
//
// It searches the bytes as they stand, which catches a bare DER or raw-scalar
// dump — and then unwraps and searches again, which is the part that actually
// earns its keep. PEM and Secret data values are both base64 armour: none of
// the needles appears literally inside either, so a key re-encoded SEC1 ->
// PKCS#8 and wrapped back up is invisible to a substring search over the
// armour. That is precisely the leak this test exists to catch, and searching
// the armour alone would report a clean bill of health over it.
func findCAKeyMaterial(blob []byte, needles []caKeyNeedle) string {
	// Two layers is what the real encodings need — a Secret data value is
	// base64 of PEM, which is base64 of the DER the needles live in — and the
	// third is slack for a leak that wraps once more. It stays bounded because
	// every extra layer multiplies the work.
	const maxUnwrap = 3

	layer := [][]byte{blob}
	for depth := 0; depth <= maxUnwrap && len(layer) > 0; depth++ {
		var next [][]byte
		for _, c := range layer {
			for _, n := range needles {
				if bytes.Contains(c, n.enc) {
					return n.what
				}
			}
			for rest := c; len(rest) > 0; {
				var block *pem.Block
				block, rest = pem.Decode(rest)
				if block == nil {
					break
				}
				next = append(next, block.Bytes)
			}
			if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(c))); err == nil && len(raw) > 0 {
				next = append(next, raw)
			}
		}
		layer = next
	}
	return ""
}

// forEachJSONString calls fn for every string anywhere in a decoded JSON
// document — object values, array elements, at any depth. A scan that only
// looked at the fields expected to carry key material would wave through a leak
// that chose any other field, which is exactly the leak nobody would think to
// look for.
func forEachJSONString(v any, fn func(string)) {
	switch t := v.(type) {
	case string:
		fn(t)
	case []any:
		for _, e := range t {
			forEachJSONString(e, fn)
		}
	case map[string]any:
		for _, e := range t {
			forEachJSONString(e, fn)
		}
	}
}

// A second run must change nothing. A CA that rotated on every helm upgrade
// would break every handshake in the mesh while every pod stayed green.
func TestSecondRunChangesNothing(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	before := map[string]map[string][]byte{}
	for _, n := range f.names() {
		before[n] = f.data(n)
	}
	firstRunRequests := len(f.recorded())

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	for n, want := range before {
		got := f.data(n)
		for k, wv := range want {
			if !bytes.Equal(got[k], wv) {
				t.Errorf("second run changed %s[%q]", n, k)
			}
		}
	}
	if n := f.countRequests(http.MethodPut, ""); n != 0 {
		t.Errorf("second run issued %d PUTs; an unchanged install must write nothing", n)
	}
	// A run that is going to keep what is already there must not have put a
	// freshly minted private key on the wire to find that out.
	for _, r := range f.recorded()[firstRunRequests:] {
		if r.Method != http.MethodGet {
			t.Errorf("second run issued %s %s; it should only have read", r.Method, r.Path)
		}
	}
}

// Several Jobs starting together — a retried Job, or one per service — must
// converge on one CA and one Secret each, not fight over them.
func TestConcurrentRunsConverge(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = run(context.Background(), k8sOpts(f, "flo"))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent run %d failed: %v", i, err)
		}
	}
	caPEM := f.data("flo-ca")[keyCACert]
	for _, svc := range []string{"api", "launch", "runner"} {
		d := f.data("flo-" + svc)
		verifyLeaf(t, caPEM, d[keyTLSCert])
		if !bytes.Equal(d[keyCACert], caPEM) {
			t.Errorf("flo-%s carries a ca.pem the other services do not have", svc)
		}
	}
}

// A per-service Secret that is already there is only safe to leave alone once
// it has been looked at. Every case below used to exit 0 and hand the operator
// a mesh that cannot handshake.
func TestStaleServiceSecretIsRejected(t *testing.T) {
	// The one that actually happens: <prefix>-ca is create-once, so deleting
	// it is the only way to rotate the root. Do that, re-run, and the
	// per-service Secrets still hold leaves signed by the CA that is gone.
	t.Run("CA rotated underneath it", func(t *testing.T) {
		f := newFakeAPI(t, "flomation")
		withServiceAccount(t, f, nil)

		if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
			t.Fatalf("run 1: %v", err)
		}
		f.drop("flo-ca")

		err := run(context.Background(), k8sOpts(f, "flo"))
		if err == nil {
			t.Fatal("run exited 0 leaving flo-api signed by a CA that is no longer the trust root")
		}
		if !strings.Contains(err.Error(), "does not chain") || !strings.Contains(err.Error(), "-k8s-force") {
			t.Fatalf("error names neither the cause nor the remedy: %v", err)
		}
	})

	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Real CA")
	otherCertPEM, otherKeyPEM, _ := mintTestCA(t, "Someone Else's CA")
	goodCert, goodKey := mintTestLeaf(t, caCertPEM, caKeyPEM, "api")
	foreignCert, foreignKey := mintTestLeaf(t, otherCertPEM, otherKeyPEM, "api")
	_, strayKey := mintTestLeaf(t, caCertPEM, caKeyPEM, "api")

	cases := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{"not a certificate at all", map[string][]byte{
			keyCACert: caCertPEM, keyTLSCert: []byte("garbage"), keyTLSKey: []byte("garbage"),
		}, "is not PEM"},
		{"no cert or key", map[string][]byte{keyCACert: caCertPEM}, "has no tls.crt/tls.key"},
		{"signed by another CA", map[string][]byte{
			keyCACert: caCertPEM, keyTLSCert: foreignCert, keyTLSKey: foreignKey,
		}, "does not chain"},
		{"cert and key are not a pair", map[string][]byte{
			keyCACert: caCertPEM, keyTLSCert: goodCert, keyTLSKey: strayKey,
		}, "are not a pair"},
		{"ca.pem is not the CA in use", map[string][]byte{
			keyCACert: otherCertPEM, keyTLSCert: goodCert, keyTLSKey: goodKey,
		}, "does not contain the CA in use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, "flomation")
			withServiceAccount(t, f, nil)
			f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})
			f.put("flo-api", tc.data)

			err := run(context.Background(), k8sOpts(f, "flo"))
			if err == nil {
				t.Fatal("run exited 0 over a Secret the mesh cannot use")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not say %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "-k8s-force") {
				t.Fatalf("error does not name the remedy: %v", err)
			}
		})
	}

	// Every error above tells the operator to pass -k8s-force, so that had
	// better be a remedy and not just a suggestion.
	t.Run("-k8s-force is the remedy it claims to be", func(t *testing.T) {
		for _, tc := range cases {
			f := newFakeAPI(t, "flomation")
			withServiceAccount(t, f, nil)
			f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})
			f.put("flo-api", tc.data)

			opts := k8sOpts(f, "flo")
			opts.K8sForce = true
			if err := run(context.Background(), opts); err != nil {
				t.Fatalf("%s: -k8s-force did not repair it: %v", tc.name, err)
			}
			repaired := f.data("flo-api")
			verifyLeaf(t, caCertPEM, repaired[keyTLSCert])
			if !bytes.Equal(repaired[keyCACert], caCertPEM) {
				t.Fatalf("%s: ca.pem was not repaired", tc.name)
			}
		}
	})

	// And a Secret that is genuinely fine is still left alone.
	t.Run("a healthy secret is kept", func(t *testing.T) {
		f := newFakeAPI(t, "flomation")
		withServiceAccount(t, f, nil)
		f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})
		f.put("flo-api", map[string][]byte{keyCACert: caCertPEM, keyTLSCert: goodCert, keyTLSKey: goodKey})

		if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
			t.Fatalf("run rejected a healthy Secret: %v", err)
		}
		if !bytes.Equal(f.data("flo-api")[keyTLSCert], goodCert) {
			t.Fatal("a healthy Secret was rewritten")
		}
	})
}

// An expired leaf is the same silent failure as a stale one: the pod mounts it,
// starts, reports Ready, and fails every handshake.
func TestExpiredServiceSecretIsRejected(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Stable CA")
	caCert, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("parseCA: %v", err)
	}
	// NotBefore is always now-1h, so a negative validity expires it.
	expiredCert, expiredKey, err := generateServiceCert(
		genOpts{CAOrg: "Flomation", CertValidity: -30 * time.Minute},
		serviceSpec{Name: "api", CN: "api"}, caCert, caKey)
	if err != nil {
		t.Fatalf("generateServiceCert: %v", err)
	}

	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})
	f.put("flo-api", map[string][]byte{keyCACert: caCertPEM, keyTLSCert: expiredCert, keyTLSKey: expiredKey})

	err = run(context.Background(), k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("run exited 0 over an expired certificate")
	}
	if !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "-k8s-force") {
		t.Fatalf("error names neither the expiry nor the remedy: %v", err)
	}
}

// An expired CA in <prefix>-ca is the stale-leaf failure one level up: every
// certificate minted under it is dead on arrival.
func TestExpiredCASecretIsRejected(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCert, caKey, err := generateCA(genOpts{CACN: "Expired CA", CAOrg: "Flomation", CAValidity: -30 * time.Minute})
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	caKeyPEM, err := encodeECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	f.put("flo-ca", map[string][]byte{keyCACert: encodePEM("CERTIFICATE", caCert.Raw), keyCAKey: caKeyPEM})

	err = run(context.Background(), k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("run adopted an expired CA and reported success")
	}
	if !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "delete the secret") {
		t.Fatalf("error names neither the expiry nor the remedy: %v", err)
	}
	if f.exists("flo-api") {
		t.Fatal("leaves were minted under an expired root")
	}
}

// -k8s-api must be https. Over plain http the ServiceAccount token crosses the
// pod network in the clear and nothing authenticates the server.
func TestPlainHTTPAPIServerIsRefused(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	var reached string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = r.Header.Get("Authorization")
		writeStatus(w, http.StatusNotFound, "NotFound", "x")
	}))
	defer plain.Close()

	opts := k8sOpts(f, "flo")
	opts.K8sAPI = plain.URL
	err := run(context.Background(), opts)
	if err == nil {
		t.Fatal("a plain-http API server was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error does not say what is wrong: %v", err)
	}
	if reached != "" {
		t.Fatalf("the token was sent anyway: %q", reached)
	}

	if _, err := newK8sClient("https://user:pass@10.0.0.1:6443", "flomation"); err == nil {
		t.Fatal("an API server URL with embedded credentials was accepted")
	}
}

// net/http forwards Authorization across a redirect when the host matches, and
// it matches on hostname alone — no port, no scheme. Following one would hand
// the token to whatever answered, so the client does not follow any.
func TestRedirectIsRefused(t *testing.T) {
	var leaked string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		writeStatus(w, http.StatusNotFound, "NotFound", "x")
	}))
	defer sink.Close()

	f := newFakeAPI(t, "flomation")
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	withServiceAccountCert(t, redirector.Certificate().Raw, "flomation")

	opts := k8sOpts(f, "flo")
	opts.K8sAPI = redirector.URL
	err := run(context.Background(), opts)
	if err == nil {
		t.Fatal("run followed a redirect and reported success")
	}
	if leaked != "" {
		t.Fatalf("the token followed the redirect to %s: %q", sink.URL, leaked)
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("error does not name the redirect: %v", err)
	}
}

// Every API failure must be a non-zero exit with a message that names what to
// fix — 403 in particular, which is the RBAC the chart has to get right.
func TestAPIFailuresAreReported(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want string
	}{
		{"401", http.StatusUnauthorized,
			`{"kind":"Status","message":"Unauthorized","reason":"Unauthorized","code":401}`, "Unauthorized"},
		{"403", http.StatusForbidden,
			`{"kind":"Status","message":"secrets is forbidden: User \"system:serviceaccount:flomation:gencerts\" cannot create resource \"secrets\"","reason":"Forbidden","code":403}`,
			"cannot create resource"},
		{"500", http.StatusInternalServerError,
			`{"kind":"Status","message":"etcd unavailable","reason":"InternalError","code":500}`, "etcd unavailable"},
		{"malformed body", http.StatusOK, `{not json`, "parse Secret"},
		{"proxy error page", http.StatusBadGateway, `<html>502 bad gateway</html>`, "withheld"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, "flomation")
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			withServiceAccountCert(t, srv.Certificate().Raw, "flomation")

			opts := k8sOpts(f, "flo")
			opts.K8sAPI = srv.URL
			err := run(context.Background(), opts)
			if err == nil {
				t.Fatalf("run returned nil on %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// No ServiceAccount volume is the "you forgot automountServiceAccountToken"
// case; it must say so rather than fail later as a connection error.
func TestMissingServiceAccountVolumeIsReported(t *testing.T) {
	dir := t.TempDir()
	old := saDir
	saDir = dir
	t.Cleanup(func() { saDir = old })

	_, err := newK8sClient("https://10.0.0.1:6443", "flomation")
	if err == nil {
		t.Fatal("a client was built with no ServiceAccount volume")
	}
	if !strings.Contains(err.Error(), "ServiceAccount token") {
		t.Fatalf("error does not name the missing file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newK8sClient("https://10.0.0.1:6443", "flomation"); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty token was accepted or misreported: %v", err)
	}
}

// -ca-cert pins a CA that is never stored in <prefix>-ca. A later run that
// forgets the flag mints a fresh root — which must fail loudly on the first
// per-service Secret, not quietly split the mesh in two.
func TestPinnedCAIsNotSilentlyReplaced(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	src := t.TempDir()
	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Pinned CA")
	certFile, keyFile := filepath.Join(src, "ca.pem"), filepath.Join(src, "ca-key.pem")
	if err := writePEMFile(certFile, caCertPEM); err != nil {
		t.Fatal(err)
	}
	if err := writePEMFile(keyFile, caKeyPEM); err != nil {
		t.Fatal(err)
	}

	opts := k8sOpts(f, "flo")
	opts.CACertFile, opts.CAKeyFile = certFile, keyFile
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run with -ca-cert: %v", err)
	}
	if f.exists("flo-ca") {
		t.Fatal("a pinned CA's private key was copied into flo-ca")
	}
	verifyLeaf(t, caCertPEM, f.data("flo-api")[keyTLSCert])

	// Same run, flag dropped.
	err := run(context.Background(), k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("dropping -ca-cert minted a new root and reported success")
	}
	if !strings.Contains(err.Error(), "does not chain") {
		t.Fatalf("error does not explain the split: %v", err)
	}
}

// withServiceAccountCert is withServiceAccount for a server that is not a
// fakeAPI.
func withServiceAccountCert(t *testing.T, caDER []byte, namespace string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string][]byte{
		"token":     []byte("test-token\n"),
		"ca.crt":    encodePEM("CERTIFICATE", caDER),
		"namespace": []byte(namespace + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	old := saDir
	saDir = dir
	t.Cleanup(func() { saDir = old })
}

// An existing CA Secret is adopted, never regenerated: rotating the trust root
// on a helm upgrade would break every handshake in the mesh while every pod
// stayed Running.
func TestExistingCASecretIsReusedNotRegenerated(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, caKeyPEM, caCert := mintTestCA(t, "Pre-existing CA")
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run: %v", err)
	}

	after := f.data("flo-ca")
	if string(after[keyCACert]) != string(caCertPEM) || string(after[keyCAKey]) != string(caKeyPEM) {
		t.Fatal("the CA Secret was rewritten; it must be adopted untouched")
	}
	if n := f.countRequests(http.MethodPut, "flo-ca"); n != 0 {
		t.Fatalf("%d PUTs against the CA Secret; it must never be updated", n)
	}

	for _, svc := range []string{"api", "launch", "runner"} {
		data := f.data("flo-" + svc)
		verifyLeaf(t, caCertPEM, data[keyTLSCert])
		if string(data[keyCACert]) != string(caCertPEM) {
			t.Fatalf("flo-%s carries a different ca.pem than the CA Secret", svc)
		}
	}

	// And the leaves really chain to the pre-existing CA, not a fresh one.
	block, _ := pem.Decode(f.data("flo-api")[keyTLSCert])
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Issuer.CommonName != caCert.Subject.CommonName {
		t.Fatalf("leaf issuer = %q, want %q", leaf.Issuer.CommonName, caCert.Subject.CommonName)
	}
}

// Two Jobs racing must converge. The loser of the create sees 409 Conflict,
// which is success: it re-reads the winner's CA and mints its leaves from that.
func TestCACreateConflictIsTreatedAsSuccess(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	// The CA exists, but our first read misses it — exactly what a Job that
	// checked a moment too early sees.
	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Winner CA")
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})
	f.hideGets["flo-ca"] = 1

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run must treat 409 on the CA Secret as success: %v", err)
	}

	if n := f.countRequests(http.MethodPost, ""); n == 0 {
		t.Fatal("expected a POST that conflicted")
	}
	after := f.data("flo-ca")
	if string(after[keyCAKey]) != string(caKeyPEM) {
		t.Fatal("the racing run overwrote the winner's CA key")
	}
	for _, svc := range []string{"api", "launch", "runner"} {
		verifyLeaf(t, caCertPEM, f.data("flo-" + svc)[keyTLSCert])
	}
}

// A SIGTERM during the CA Secret's create lands in the worst window there is:
// the API server has committed the object, the client never read the response.
// The run must fail loudly — and the run after it must adopt the CA that is now
// in etcd, because minting a second root would sign half the mesh with a key
// the other half has never seen, with every pod Running throughout.
// There are two ways back from that window, and both must land on the same
// root: the next run's read finds the committed object, or it misses and the
// POST comes back 409 Conflict. The subtests cover one each.
func TestCancelledCACreateIsAdoptedByTheNextRun(t *testing.T) {
	// cancelDuringCACreate runs once, cancelled in the commit window, and
	// returns the CA the API server was left holding.
	cancelDuringCACreate := func(t *testing.T, f *fakeAPI) map[string][]byte {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		f.setOnCommit(func(r *http.Request, name string) {
			if name != caSecretName("flo") {
				return
			}
			// Cancel, then wait for the server side to see the client leave.
			// Returning any earlier would race the response: the client could
			// read the 201 and never observe the cancellation this is about.
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				t.Error("the client never abandoned the request; this test is not exercising the window it claims to")
			}
		})

		err := run(ctx, k8sOpts(f, "flo"))
		if err == nil {
			t.Fatal("a run cancelled mid-create reported success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error is not identifiable as a cancellation, so main cannot report it as one: %v", err)
		}
		if !f.exists("flo-ca") {
			t.Fatal("the CA Secret was never committed; this test is not exercising the window it claims to")
		}
		committed := f.data("flo-ca")
		if _, _, err := parseCA(committed[keyCACert], committed[keyCAKey]); err != nil {
			t.Fatalf("the committed CA Secret is not a usable CA: %v", err)
		}
		f.setOnCommit(nil)
		return committed
	}

	// assertAdopted checks the re-run kept the committed root and minted every
	// leaf under it.
	assertAdopted := func(t *testing.T, f *fakeAPI, committed map[string][]byte) {
		t.Helper()
		after := f.data("flo-ca")
		if !bytes.Equal(after[keyCACert], committed[keyCACert]) || !bytes.Equal(after[keyCAKey], committed[keyCAKey]) {
			t.Fatal("the re-run replaced the CA the cancelled run had already committed; a second root splits the mesh")
		}
		if n := f.countRequests(http.MethodPut, "flo-ca"); n != 0 {
			t.Fatalf("%d PUTs against the CA Secret; it must never be updated", n)
		}
		for _, svc := range []string{"api", "launch", "runner"} {
			data := f.data("flo-" + svc)
			verifyLeaf(t, committed[keyCACert], data[keyTLSCert])
			if !bytes.Equal(data[keyCACert], committed[keyCACert]) {
				t.Fatalf("flo-%s carries a ca.pem that is not the adopted root", svc)
			}
		}
	}

	// The ordinary recovery: the object is there, the next run reads it.
	t.Run("the next run reads the committed CA back", func(t *testing.T) {
		f := newFakeAPI(t, "flomation")
		withServiceAccount(t, f, nil)

		committed := cancelDuringCACreate(t, f)
		if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
			t.Fatalf("the re-run did not recover from the cancelled create: %v", err)
		}
		assertAdopted(t, f, committed)
	})

	// The recovery the review asked about: the next run's read misses — a
	// stale cache, or simply a Job that looked a moment too early — so it
	// tries to create and is told 409 Conflict. Treating that as success is
	// the only thing standing between this window and a second trust root.
	t.Run("the next run's read misses and the create conflicts", func(t *testing.T) {
		f := newFakeAPI(t, "flomation")
		withServiceAccount(t, f, nil)

		committed := cancelDuringCACreate(t, f)

		postsBefore := f.countRequests(http.MethodPost, "")
		f.hideGets["flo-ca"] = 1
		if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
			t.Fatalf("409 on the CA Secret was not treated as success: %v", err)
		}
		if f.countRequests(http.MethodPost, "") <= postsBefore {
			t.Fatal("the re-run never POSTed, so the 409 path was not exercised")
		}
		assertAdopted(t, f, committed)
	})
}

// The same cancellation one step later: the CA is safely stored and the run is
// interrupted while writing per-service Secrets. It must still exit non-zero —
// a Job that stopped halfway and said nothing leaves pods that start, report
// Ready, and fail every handshake.
func TestCancelledServiceWriteStillFails(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Stable CA")
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})

	ctx, cancel := context.WithCancel(context.Background())
	f.setOnCommit(func(r *http.Request, name string) {
		if name != serviceSecretName("flo", "api") {
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
			t.Error("the client never abandoned the request")
		}
	})

	err := run(ctx, k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("a run cancelled part-way through the service Secrets reported success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error is not identifiable as a cancellation: %v", err)
	}
	// The services after the one that was interrupted were never provisioned:
	// that is precisely why the exit has to be non-zero.
	if f.exists("flo-runner") {
		t.Fatal("the run kept going after cancellation")
	}
}

// The request body has to be a Secret the API server would accept: PEM is
// multi-line, so it travels base64-encoded in `data`, never raw.
func TestSecretRequestBodyIsWellFormed(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run: %v", err)
	}

	var body []byte
	for _, r := range f.recorded() {
		if r.Method == http.MethodPost && strings.Contains(string(r.Body), `"flo-api"`) {
			body = r.Body
		}
	}
	if body == nil {
		t.Fatal("no POST for flo-api was recorded")
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if raw["apiVersion"] != "v1" || raw["kind"] != "Secret" || raw["type"] != "Opaque" {
		t.Fatalf("apiVersion/kind/type = %v/%v/%v", raw["apiVersion"], raw["kind"], raw["type"])
	}
	meta, ok := raw["metadata"].(map[string]any)
	if !ok || meta["name"] != "flo-api" || meta["namespace"] != "flomation" {
		t.Fatalf("metadata = %v", raw["metadata"])
	}
	if _, present := meta["resourceVersion"]; present {
		t.Fatal("a create must not carry a resourceVersion")
	}

	// No raw PEM anywhere in the body: it would mean stringData-style escaping
	// with a real chance of a mangled key.
	if strings.Contains(string(body), "-----BEGIN") {
		t.Fatal("body contains raw PEM; data values must be base64")
	}

	data, ok := raw["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v", raw["data"])
	}
	for _, k := range []string{keyCACert, keyTLSCert, keyTLSKey} {
		v, ok := data[k].(string)
		if !ok {
			t.Fatalf("data[%q] missing or not a string", k)
		}
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			t.Fatalf("data[%q] is not standard base64: %v", k, err)
		}
		block, _ := pem.Decode(decoded)
		if block == nil {
			t.Fatalf("data[%q] does not decode to a PEM block", k)
		}
		if lines := strings.Count(strings.TrimSpace(string(decoded)), "\n"); lines < 2 {
			t.Fatalf("data[%q] decoded to %d lines; a real PEM block is multi-line", k, lines+1)
		}
	}
}

// Without -k8s-force an existing per-service Secret is left alone; with it, the
// leaf is replaced — and the CA Secret is untouched either way.
func TestForceOnlyRenewsServiceSecrets(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, caKeyPEM, _ := mintTestCA(t, "Stable CA")
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: caKeyPEM})

	// A real leaf, signed by that CA — what a previous install left behind. It
	// has to be a usable one: an unusable Secret is now a hard error in its own
	// right (see TestStaleServiceSecretIsRejected), so a placeholder string
	// here would test the wrong thing.
	oldCertPEM, oldKeyPEM := mintTestLeaf(t, caCertPEM, caKeyPEM, "api")
	f.put("flo-api", map[string][]byte{keyCACert: caCertPEM, keyTLSCert: oldCertPEM, keyTLSKey: oldKeyPEM})

	opts := k8sOpts(f, "flo")
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := f.data("flo-api")[keyTLSCert]; string(got) != string(oldCertPEM) {
		t.Fatal("flo-api was overwritten without -k8s-force")
	}
	if n := f.countRequests(http.MethodPut, ""); n != 0 {
		t.Fatalf("%d PUTs without -k8s-force; existing Secrets must be left alone", n)
	}

	opts.K8sForce = true
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run with -k8s-force: %v", err)
	}
	renewed := f.data("flo-api")
	if string(renewed[keyTLSCert]) == string(oldCertPEM) {
		t.Fatal("-k8s-force did not renew flo-api")
	}
	verifyLeaf(t, caCertPEM, renewed[keyTLSCert])
	if string(renewed[keyCAKey]) != "" {
		t.Fatal("renewal put the CA key into a per-service Secret")
	}

	if n := f.countRequests(http.MethodPut, "flo-ca"); n != 0 {
		t.Fatalf("%d PUTs against the CA Secret; it must never be updated", n)
	}
	if string(f.data("flo-ca")[keyCAKey]) != string(caKeyPEM) {
		t.Fatal("-k8s-force rotated the CA")
	}
}

// The API server's certificate is always verified. Skipping verification here
// would let anything on the pod network hand the mesh a trust root.
func TestAPIServerCertificateIsVerified(t *testing.T) {
	f := newFakeAPI(t, "flomation")

	// A valid CA, but not the one that signed the fake API server.
	otherCA, _, _ := mintTestCA(t, "Someone Else's CA")
	withServiceAccount(t, f, otherCA)

	err := run(context.Background(), k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("run succeeded against an API server it could not authenticate")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("error does not look like a TLS verification failure: %v", err)
	}
	if n := f.storedCount(); n != 0 {
		t.Fatalf("%d secrets written despite the failed handshake", n)
	}
}

// A failure to write must be a non-zero exit; run() reporting nil would let the
// chart's Job pass while the mesh has no certificates.
func TestAPIErrorIsReportedNotSwallowed(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	opts := k8sOpts(f, "flo")
	opts.K8sNamespace = "wrong-namespace" // every call 404s on an unexpected path

	err := run(context.Background(), opts)
	if err == nil {
		t.Fatal("run returned nil after failing to write any Secret")
	}
}

// The namespace defaults to the pod's own, read from the ServiceAccount volume.
func TestNamespaceDefaultsToThePodsOwn(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	opts := k8sOpts(f, "flo")
	opts.K8sNamespace = ""
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, r := range f.recorded() {
		if !strings.HasPrefix(r.Path, "/api/v1/namespaces/flomation/secrets") {
			t.Fatalf("request to %s, want the pod's own namespace", r.Path)
		}
	}
}

// A CA Secret pre-provisioned with a PKCS#8 key (what openssl and cert-manager
// write) is usable: refusing it would fail an install over an encoding.
func TestPKCS8CAKeyFromSecretIsAccepted(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, sec1PEM, _ := mintTestCA(t, "PKCS8 CA")
	key, err := parseECPrivateKey(sec1PEM)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS#8: %v", err)
	}
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: encodePEM("PRIVATE KEY", pkcs8)})

	if err := run(context.Background(), k8sOpts(f, "flo")); err != nil {
		t.Fatalf("run with a PKCS#8 CA key: %v", err)
	}
	verifyLeaf(t, caCertPEM, f.data("flo-api")[keyTLSCert])
}

// A CA Secret whose cert and key are not a pair must fail here, not at the
// first handshake in a pod that has already started.
func TestMismatchedCASecretIsRejected(t *testing.T) {
	f := newFakeAPI(t, "flomation")
	withServiceAccount(t, f, nil)

	caCertPEM, _, _ := mintTestCA(t, "Cert Half")
	_, otherKeyPEM, _ := mintTestCA(t, "Key Half")
	f.put("flo-ca", map[string][]byte{keyCACert: caCertPEM, keyCAKey: otherKeyPEM})

	err := run(context.Background(), k8sOpts(f, "flo"))
	if err == nil {
		t.Fatal("run accepted a CA Secret whose key does not match its certificate")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// An error body that is not a Status must not be echoed: this client POSTs
// private keys, and an intermediary that reflects a request body would
// otherwise put one in the Job log.
func TestAPIErrorNeverEchoesAnUnparseableBody(t *testing.T) {
	err := apiError(http.StatusInternalServerError, []byte("proxy error, your request was: -----BEGIN EC PRIVATE KEY-----\nMHc\n"))
	if strings.Contains(err.Error(), "BEGIN EC PRIVATE KEY") || strings.Contains(err.Error(), "MHc") {
		t.Fatalf("error echoed the body: %v", err)
	}

	statusErr := apiError(http.StatusForbidden, []byte(`{"kind":"Status","message":"secrets is forbidden","reason":"Forbidden","code":403}`))
	if !strings.Contains(statusErr.Error(), "secrets is forbidden") {
		t.Fatalf("Status message was dropped: %v", statusErr)
	}
}

func TestValidateSecretNames(t *testing.T) {
	svcs := []serviceSpec{{Name: "api", CN: "api"}}
	if err := validateSecretNames("flomation-mtls", svcs); err != nil {
		t.Fatalf("valid names rejected: %v", err)
	}
	for _, bad := range []string{"Flo", "flo_mtls", "-flo", "flo."} {
		if err := validateSecretNames(bad, svcs); err == nil {
			t.Fatalf("prefix %q was accepted; the API server would 422", bad)
		}
	}
	if err := validateSecretNames("flo", []serviceSpec{{Name: "API", CN: "api"}}); err == nil {
		t.Fatal("service name \"API\" was accepted; the API server would 422")
	}
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
