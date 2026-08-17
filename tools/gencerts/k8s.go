// Kubernetes Secret output for gencerts.
//
// The chart that runs this tool ships to customer clusters that may have no
// cert-manager, so a one-shot Job mints the mesh's mTLS material itself — and
// something has to put the result into Secrets. The obvious ways were all
// rejected on measurement: kubectl in the image costs +56.8 MB on a 121 MB
// image for a tool used once per install; `apk add kubectl` at Job runtime
// needs root, which the Pod Security Admission "restricted" profile the chart
// requires forbids, plus a package mirror an air-gapped customer does not have;
// client-go drags a large dependency tree in for a single POST.
//
// So this file speaks to the API server the way the executor's
// actions/infrastructure/kubernetes package does: plain net/http, a Bearer
// token from the pod's ServiceAccount, the cluster CA from the same projected
// volume, and the Status envelope decoded for error messages an operator can
// act on. Nothing here logs secret material — Secret names and actions only.

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// saVolume is where the kubelet projects the pod's ServiceAccount: the
	// token we authenticate with, the CA that signs the API server, and the
	// pod's own namespace.
	saVolume = "/var/run/secrets/kubernetes.io/serviceaccount"

	// k8sTimeout bounds a single API call. Secrets are small; anything slower
	// than this is a broken network path, and the Job should fail visibly
	// rather than hang until the cluster's activeDeadlineSeconds.
	k8sTimeout = 30 * time.Second

	// maxResponseBody caps a response body. The API server refuses a Secret
	// larger than 1 MiB, so anything past this cap is a proxy or an error page,
	// not an object.
	maxResponseBody = 2 << 20
)

// Secret data keys. tls.crt/tls.key are the kubernetes.io/tls names, so a
// per-service Secret stays consumable as that type if it is ever wanted; the
// Secrets themselves are Opaque.
const (
	keyCACert  = "ca.pem"
	keyCAKey   = "ca-key.pem"
	keyTLSCert = "tls.crt"
	keyTLSKey  = "tls.key"
)

// saDir is a var, not a const, so tests can point the ServiceAccount lookups at
// a temporary directory. Nothing in production reassigns it.
var saDir = saVolume

// dns1123Label matches the names the API server will accept for a Secret.
// Checking up front turns a confusing 422 from the server into a message that
// names the flag at fault.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateSecretNames checks every Secret name this run would create.
func validateSecretNames(prefix string, services []serviceSpec) error {
	names := []string{caSecretName(prefix)}
	for _, svc := range services {
		names = append(names, serviceSecretName(prefix, svc.Name))
	}
	for _, n := range names {
		if len(n) > 253 || !dns1123Label.MatchString(n) {
			return fmt.Errorf("secret name %q is not a valid Kubernetes name: -k8s-secret-prefix and -service must be lowercase alphanumeric or '-'", n)
		}
	}
	return nil
}

func caSecretName(prefix string) string { return prefix + "-ca" }

func serviceSecretName(prefix, service string) string { return prefix + "-" + service }

// k8sClient is a minimal REST client for Secrets in one namespace.
type k8sClient struct {
	server    string // scheme://host:port, no trailing slash
	namespace string
	token     string
	http      *http.Client
}

// newK8sClient builds the in-pod client: ServiceAccount token for authn, the
// cluster CA for authenticating the server.
func newK8sClient(apiOverride, namespaceOverride string) (*k8sClient, error) {
	server := strings.TrimRight(strings.TrimSpace(apiOverride), "/")
	if server == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, errors.New("not running in a Kubernetes pod (KUBERNETES_SERVICE_HOST/KUBERNETES_SERVICE_PORT unset); pass -k8s-api to override")
		}
		server = "https://" + net.JoinHostPort(host, port)
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid API server URL %q", server)
	}
	// https only. Everything below — RootCAs, the token in the Authorization
	// header — is worth nothing over plain http: the token would cross the pod
	// network in the clear and anything that answered would be believed. A
	// typo'd -k8s-api must fail here, not silently downgrade.
	if u.Scheme != "https" {
		return nil, fmt.Errorf("API server URL %q must use https: over %s the ServiceAccount token travels in cleartext and the server is never authenticated", server, u.Scheme)
	}
	// Credentials in the URL would be the one thing net/http puts into a
	// *url.Error, i.e. into the Job log. There is no reason to accept them.
	if u.User != nil {
		return nil, errors.New("API server URL must not embed credentials; authentication is the ServiceAccount token")
	}

	// #nosec G304 — fixed path inside the projected ServiceAccount volume;
	// saDir is only ever redirected by this package's own tests.
	token, err := os.ReadFile(filepath.Join(saDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read ServiceAccount token: %w", err)
	}
	if len(bytes.TrimSpace(token)) == 0 {
		return nil, fmt.Errorf("ServiceAccount token at %s is empty", filepath.Join(saDir, "token"))
	}

	// #nosec G304 — same projected volume, same reasoning as the token above.
	caPEM, err := os.ReadFile(filepath.Join(saDir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("cluster CA at %s contains no usable PEM certificate", filepath.Join(saDir, "ca.crt"))
	}

	namespace := strings.TrimSpace(namespaceOverride)
	if namespace == "" {
		// #nosec G304 — same projected volume, same reasoning as the token above.
		raw, err := os.ReadFile(filepath.Join(saDir, "namespace"))
		if err != nil {
			return nil, fmt.Errorf("read pod namespace: %w (pass -k8s-namespace to override)", err)
		}
		namespace = strings.TrimSpace(string(raw))
	}
	if namespace == "" {
		return nil, errors.New("namespace is empty; pass -k8s-namespace")
	}

	return &k8sClient{
		server:    server,
		namespace: namespace,
		token:     string(bytes.TrimSpace(token)),
		http: &http.Client{
			Timeout: k8sTimeout,
			// net/http forwards the Authorization header across a redirect
			// whenever the host matches, and it compares hosts without the
			// port or the scheme — so an https->http redirect back to the same
			// address would hand the ServiceAccount token to whatever is
			// listening there, in the clear. The Kubernetes API never redirects
			// a Secret request, so refusing outright costs nothing.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Redacted())
			},
			Transport: &http.Transport{
				// InsecureSkipVerify is deliberately absent and must stay
				// absent. This is the one connection that hands us the mesh's
				// trust root; skipping verification would let anything on the
				// pod network serve us one of its own.
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
			},
		},
	}, nil
}

// secret is the wire shape of a core/v1 Secret, cut down to what this tool
// reads and writes.
type secret struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   secretMeta `json:"metadata"`
	Type       string     `json:"type"`
	// Data is base64 per the Secret schema. It is used in preference to
	// stringData because PEM is multi-line: base64 keeps every value a single
	// flat JSON string with no escaping to get wrong on either side.
	Data map[string]string `json:"data"`
}

type secretMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
}

func (c *k8sClient) secretBody(name, resourceVersion string, data map[string][]byte) ([]byte, error) {
	encoded := make(map[string]string, len(data))
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString(v)
	}

	return json.Marshal(secret{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: secretMeta{
			Name:            name,
			Namespace:       c.namespace,
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "gencerts"},
			ResourceVersion: resourceVersion,
		},
		Type: "Opaque",
		Data: encoded,
	})
}

func (c *k8sClient) secretPath(name string) string {
	p := "/api/v1/namespaces/" + url.PathEscape(c.namespace) + "/secrets"
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p
}

// do performs one API call. The token travels in a header and never in the URL,
// so no error path here can echo it.
func (c *k8sClient) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.server+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("kubernetes API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, raw, nil
}

// status is the Kubernetes error envelope, returned for every non-2xx.
type status struct {
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

// apiError turns a non-2xx into something an operator can act on. Kubernetes'
// own message ("secrets is forbidden: User ... cannot create resource") is far
// more useful than the status code, so it leads.
//
// A body that does not parse as a Status is deliberately NOT echoed. This
// client POSTs private keys, and an intermediary that reflects a request body
// into an error page would otherwise put a CA key in the Job log.
func apiError(code int, body []byte) error {
	var st status
	if err := json.Unmarshal(body, &st); err == nil && st.Message != "" {
		if st.Reason != "" {
			return fmt.Errorf("kubernetes API error (%d %s): %s", code, st.Reason, st.Message)
		}
		return fmt.Errorf("kubernetes API error (%d): %s", code, st.Message)
	}
	return fmt.Errorf("kubernetes API error (%d %s): response was not a Status object (body withheld: it may contain key material)", code, http.StatusText(code))
}

// getSecret reads one Secret. found is false on 404 — a Secret that is not
// there yet is the normal first-install case, not a failure.
func (c *k8sClient) getSecret(ctx context.Context, name string) (data map[string][]byte, resourceVersion string, found bool, err error) {
	code, body, err := c.do(ctx, http.MethodGet, c.secretPath(name), nil)
	if err != nil {
		return nil, "", false, err
	}
	if code == http.StatusNotFound {
		return nil, "", false, nil
	}
	if code < 200 || code > 299 {
		return nil, "", false, apiError(code, body)
	}

	var s secret
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, "", false, fmt.Errorf("parse Secret %s: %w", name, err)
	}

	data = make(map[string][]byte, len(s.Data))
	for k, v := range s.Data {
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, "", false, fmt.Errorf("secret %s: key %q is not valid base64: %w", name, k, err)
		}
		data[k] = decoded
	}
	return data, s.Metadata.ResourceVersion, true, nil
}

// createSecret POSTs a new Secret.
//
// created=false means the API server answered 409 Conflict: another Job got
// there first. That is success, not failure — the caller re-reads the object
// and adopts it, so two Jobs racing converge on one Secret instead of fighting
// over it.
func (c *k8sClient) createSecret(ctx context.Context, name string, data map[string][]byte) (created bool, err error) {
	body, err := c.secretBody(name, "", data)
	if err != nil {
		return false, fmt.Errorf("encode Secret %s: %w", name, err)
	}

	code, resp, err := c.do(ctx, http.MethodPost, c.secretPath(""), body)
	if err != nil {
		return false, err
	}
	switch {
	case code >= 200 && code <= 299:
		return true, nil
	case code == http.StatusConflict:
		return false, nil
	default:
		return false, apiError(code, resp)
	}
}

// replaceSecret PUTs over an existing Secret at a known resourceVersion.
//
// replaced=false means 409 Conflict: something changed the object between our
// read and our write. The caller treats that as success — the only other writer
// is another gencerts run, minting from the same adopted CA, so whichever leaf
// wins is valid.
func (c *k8sClient) replaceSecret(ctx context.Context, name, resourceVersion string, data map[string][]byte) (replaced bool, err error) {
	body, err := c.secretBody(name, resourceVersion, data)
	if err != nil {
		return false, fmt.Errorf("encode Secret %s: %w", name, err)
	}

	code, resp, err := c.do(ctx, http.MethodPut, c.secretPath(name), body)
	if err != nil {
		return false, err
	}
	switch {
	case code >= 200 && code <= 299:
		return true, nil
	case code == http.StatusConflict:
		return false, nil
	default:
		return false, apiError(code, resp)
	}
}

// ensureCASecret returns the CA to mint against, creating <prefix>-ca exactly
// once for the lifetime of the installation.
//
// Reuse is the entire point. Regenerating the CA on a helm upgrade would rotate
// the mesh's trust root while every pod stayed Running with leaves signed by
// the old one: nothing crashes, nothing restarts, handshakes simply start
// failing. So an existing Secret is read back and adopted, and this function
// only ever POSTs — never PUT, never PATCH.
func ensureCASecret(ctx context.Context, c *k8sClient, opts genOpts) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	name := caSecretName(opts.K8sSecretPrefix)

	data, _, found, err := c.getSecret(ctx, name)
	if err != nil {
		return nil, nil, fmt.Errorf("read secret %s: %w", name, err)
	}
	if found {
		caCert, caKey, err := caFromSecret(name, data)
		if err != nil {
			return nil, nil, err
		}
		// An expired root is the same silent failure as a stale leaf, one level
		// up: every certificate minted below it is dead on arrival, and the Job
		// would still exit 0. Adopting is create-once, so the remedy is to
		// delete the Secret and let the next run mint a new root.
		now := time.Now()
		if now.After(caCert.NotAfter) {
			return nil, nil, fmt.Errorf("secret %s holds a CA that expired on %s; delete the secret to mint a new one (every service Secret then needs -k8s-force)",
				name, caCert.NotAfter.UTC().Format(time.RFC3339))
		}
		if caCert.NotAfter.Before(now.Add(opts.CertValidity)) {
			// Not fatal — the leaves are still usable today — but they will die
			// with the CA rather than after -cert-validity, and nobody would
			// otherwise find that out until they did.
			fmt.Fprintf(os.Stderr, "warning: CA in %s expires on %s, before the %d-day certificates being issued under it\n",
				name, caCert.NotAfter.UTC().Format(time.RFC3339), int(opts.CertValidity.Hours()/24))
		}
		fmt.Printf("CA: reusing secret %s (CN=%s)\n", name, caCert.Subject.CommonName)
		return caCert, caKey, nil
	}

	caCert, caKey, err := generateCA(opts)
	if err != nil {
		return nil, nil, err
	}
	caKeyPEM, err := encodeECPrivateKey(caKey)
	if err != nil {
		return nil, nil, err
	}

	created, err := c.createSecret(ctx, name, map[string][]byte{
		keyCACert: encodePEM("CERTIFICATE", caCert.Raw),
		keyCAKey:  caKeyPEM,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create secret %s: %w", name, err)
	}
	if created {
		fmt.Printf("CA: %s (%s) minted, secret %s created\n", opts.CACN, opts.CAOrg, name)
		return caCert, caKey, nil
	}

	// 409: another Job created the CA between our read and our write. Adopt
	// theirs and drop the one we just minted — the object already in etcd is
	// the winner, and both Jobs must end up minting leaves against the same
	// root or half the mesh will not trust the other half.
	data, _, found, err = c.getSecret(ctx, name)
	if err != nil {
		return nil, nil, fmt.Errorf("read secret %s after conflict: %w", name, err)
	}
	if !found {
		return nil, nil, fmt.Errorf("secret %s: create returned 409 Conflict but the object is not readable", name)
	}
	caCert, caKey, err = caFromSecret(name, data)
	if err != nil {
		return nil, nil, err
	}
	fmt.Printf("CA: secret %s was created concurrently; adopting it (CN=%s)\n", name, caCert.Subject.CommonName)
	return caCert, caKey, nil
}

func caFromSecret(name string, data map[string][]byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if len(data[keyCACert]) == 0 || len(data[keyCAKey]) == 0 {
		return nil, nil, fmt.Errorf("secret %s: expected keys %q and %q", name, keyCACert, keyCAKey)
	}
	caCert, caKey, err := parseCA(data[keyCACert], data[keyCAKey])
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: %w", name, err)
	}
	return caCert, caKey, nil
}

// applyServiceSecret creates a per-service Secret and reports what it did.
//
// Without -k8s-force an existing Secret is left exactly as it is: a running pod
// has already mounted and loaded its key, so silently replacing the contents
// would only take effect at some unpredictable later restart. Renewal is an
// explicit act.
//
// It reads before it writes. Not for the create — a POST would tell us the
// object exists just as well — but because leaving a Secret alone is only safe
// once we have looked at it (see checkServiceSecret), and because the read
// keeps a freshly minted private key off the wire on every re-run that is going
// to discard it anyway.
func (c *k8sClient) applyServiceSecret(ctx context.Context, name string, data map[string][]byte, caCert *x509.Certificate, force bool) (string, error) {
	existing, resourceVersion, found, err := c.getSecret(ctx, name)
	if err != nil {
		return "", err
	}

	action := "exists, left unchanged (pass -k8s-force to renew)"
	if !found {
		created, err := c.createSecret(ctx, name, data)
		if err != nil {
			return "", err
		}
		if created {
			return "created", nil
		}

		// 409: another Job created it between our read and our write. Read the
		// winner back and hold it to the same standard as any other existing
		// Secret — it was minted by a run that adopted the same CA, so it
		// should pass, and if it does not we want to hear about it.
		existing, resourceVersion, found, err = c.getSecret(ctx, name)
		if err != nil {
			return "", fmt.Errorf("read after conflict: %w", err)
		}
		if !found {
			return "", errors.New("create returned 409 Conflict but the object is not readable; re-run the job")
		}
		action = "created concurrently by another run"
	}

	if !force {
		if err := checkServiceSecret(name, existing, caCert); err != nil {
			return "", err
		}
		return action, nil
	}

	// The resourceVersion from the read above makes the overwrite a
	// compare-and-swap rather than a blind clobber of whatever landed since.
	replaced, err := c.replaceSecret(ctx, name, resourceVersion, data)
	if err != nil {
		return "", err
	}
	if !replaced {
		return "renewed by a concurrent run", nil
	}
	return "renewed", nil
}

// checkServiceSecret decides whether an existing per-service Secret can honestly
// be left where it is.
//
// Leaving one alone unchecked is how this tool would break a mesh while
// reporting success. Delete <prefix>-ca to rotate the root — the only way,
// since the CA Secret is create-once — and re-run the Job: a new CA is minted,
// every per-service Secret still holds a leaf signed by the old one, the Job
// exits 0, and nothing goes wrong until pods that are already Running start
// failing handshakes. The same silence covers an expired leaf, a cert and key
// that are not a pair, and a Secret some other tool put there.
//
// So: if what is already in the Secret would not work, that is an error naming
// -k8s-force, not a log line.
func checkServiceSecret(name string, data map[string][]byte, caCert *x509.Certificate) error {
	certPEM, keyPEM := data[keyTLSCert], data[keyTLSKey]
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("secret %s already exists but has no %s/%s; delete it or pass -k8s-force to replace it", name, keyTLSCert, keyTLSKey)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("secret %s already exists but %s is not PEM; delete it or pass -k8s-force to replace it", name, keyTLSCert)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("secret %s: %s does not parse as a certificate (%v); pass -k8s-force to replace it", name, keyTLSCert, err)
	}

	// Checked before Verify so the message says "expired" rather than the
	// generic verification failure an expired cert also produces.
	if now := time.Now(); now.After(leaf.NotAfter) {
		return fmt.Errorf("secret %s holds a certificate that expired on %s; pass -k8s-force to renew it", name, leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("secret %s holds a certificate that does not chain to the CA in use (CN=%s): %v; pass -k8s-force to replace it", name, caCert.Subject.CommonName, err)
	}

	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("secret %s: %s and %s are not a pair (%v); pass -k8s-force to replace it", name, keyTLSCert, keyTLSKey, err)
	}

	// The service verifies its peers with the ca.pem it is handed, so that has
	// to contain the CA everyone else is being signed by. A bundle with extra
	// roots in it is fine; one without this root is not.
	if !pemContainsCert(data[keyCACert], caCert) {
		return fmt.Errorf("secret %s: %s does not contain the CA in use (CN=%s), so this service would reject its peers; pass -k8s-force to replace it", name, keyCACert, caCert.Subject.CommonName)
	}

	return nil
}

// pemContainsCert reports whether a PEM bundle carries this exact certificate.
func pemContainsCert(bundle []byte, want *x509.Certificate) bool {
	for len(bundle) > 0 {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			return false
		}
		if block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, want.Raw) {
			return true
		}
	}
	return false
}
