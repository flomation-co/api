// Command gencerts generates a self-signed CA and per-service TLS
// certificates for development and staging environments, and — with
// -k8s-secret-prefix — for a Kubernetes installation that has no cert-manager.
//
// Output structure (files, whenever -out resolves to a directory):
//
//	<output-dir>/
//	  ca.pem              Platform CA certificate
//	  ca-key.pem          CA private key (never deployed to prod)
//	  api/cert.pem        API service certificate + key
//	  launch/cert.pem     Launch service certificate + key
//	  runner/cert.pem     Runner service certificate + key
//
// Output structure (Kubernetes Secrets, when -k8s-secret-prefix is set):
//
//	<prefix>-ca           ca.pem + ca-key.pem — read by this tool only, never mounted by an app pod
//	<prefix>-<service>    ca.pem + tls.crt + tls.key
//
// That split is a security property, not a filing convenience. Every service
// needs the CA certificate to verify its peers, so ca.pem is in each
// per-service Secret — but the CA private key never leaves <prefix>-ca. If it
// did, any compromised service could mint a certificate for any other service
// and the mesh's whole trust model would be gone.
//
// Secret output is create-once. An existing <prefix>-ca is read back and its CA
// reused, never regenerated: rotating the trust root on a helm upgrade would
// silently break every handshake in the mesh while every pod stayed Running.
// Per-service Secrets are likewise left alone unless -k8s-force is passed — but
// only after they have been checked against the CA now in use. A Secret that
// would not work (wrong CA, expired, cert and key not a pair) is an error
// naming -k8s-force, because the alternative is a Job that exits 0 over a mesh
// that cannot handshake.
//
// Usage:
//
//	go run tools/gencerts/main.go [flags]
//
// Flags:
//
//	-out                Output directory (default: certs/dev; no file output when -k8s-secret-prefix is set)
//	-ca-cert            Path to existing CA certificate PEM (skips CA generation)
//	-ca-key             Path to existing CA private key PEM (required with -ca-cert)
//	-ca-cn              CA common name (default: Flomation Dev CA)
//	-ca-org             CA organisation (default: Flomation)
//	-ca-validity        CA validity in days (default: 3650)
//	-cert-validity      Service cert validity in days (default: 365)
//	-service            Service name:CN pair, repeatable (default: api,launch,runner with CN=name)
//	-san-dns            Additional DNS SAN to add to all certs, repeatable
//	-san-ip             Additional IP SAN to add to all certs, repeatable
//	-k8s-secret-prefix  Write Kubernetes Secrets <prefix>-ca and <prefix>-<service> (empty: files only)
//	-k8s-namespace      Namespace for those Secrets (default: the pod's own, from the ServiceAccount volume)
//	-k8s-api            API server https:// URL (default: from KUBERNETES_SERVICE_HOST/KUBERNETES_SERVICE_PORT)
//	-k8s-force          Overwrite existing per-service Secrets (renewal). Never applies to <prefix>-ca.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultOutDir is where files land when nothing else is asked for. It is
// applied after parsing rather than as the flag default so that a run which
// only writes Secrets writes no files at all — the chart's Job has a read-only
// root filesystem, and an unwanted MkdirAll there is a failed install.
const defaultOutDir = "certs/dev"

// errFlagParse marks a failure the flag package has already reported to the
// user. main exits non-zero without printing it a second time.
var errFlagParse = errors.New("invalid flags")

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type serviceSpec struct {
	Name string
	CN   string
}

func main() {
	opts, err := parseFlags(os.Args[0], os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		if errors.Is(err, errFlagParse) {
			// The flag package has already printed the reason and the usage.
			// Exit 2, which is what it does itself and what this tool did
			// before: a wrapper script telling a usage error apart from a
			// failed run should not have to change.
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := run(opts); err != nil {
		// Exit non-zero on every write failure: the chart's Job gates on this
		// exit code, and a Job that "succeeds" without minting certificates
		// leaves every pod after it failing TLS handshakes with no clue why.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type genOpts struct {
	OutDir       string // empty = no file output
	CACertFile   string // existing CA cert PEM (empty = generate new)
	CAKeyFile    string // existing CA key PEM (empty = generate new)
	CACN         string
	CAOrg        string
	CAValidity   time.Duration
	CertValidity time.Duration
	Services     []serviceSpec
	ExtraDNS     []string
	ExtraIPs     []net.IP

	K8sSecretPrefix string // empty = no Secret output
	K8sNamespace    string // empty = the pod's own namespace
	K8sAPI          string // empty = from KUBERNETES_SERVICE_HOST/PORT
	K8sForce        bool   // overwrite existing per-service Secrets
}

// parseFlags turns a command line into genOpts. It is separate from main so the
// defaults — which decide whether files are written at all — are testable.
func parseFlags(name string, args []string, out io.Writer) (genOpts, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)

	var (
		outDir       = fs.String("out", "", "output directory (default \""+defaultOutDir+"\"; no files when -k8s-secret-prefix is set)")
		caCertFile   = fs.String("ca-cert", "", "path to existing CA certificate PEM (skips CA generation)")
		caKeyFile    = fs.String("ca-key", "", "path to existing CA private key PEM (required with -ca-cert)")
		caCN         = fs.String("ca-cn", "Flomation Dev CA", "CA common name (ignored when -ca-cert is set)")
		caOrg        = fs.String("ca-org", "Flomation", "CA organisation (ignored when -ca-cert is set)")
		caValidity   = fs.Int("ca-validity", 3650, "CA certificate validity in days (ignored when -ca-cert is set)")
		certValidity = fs.Int("cert-validity", 365, "service certificate validity in days")
		secretPrefix = fs.String("k8s-secret-prefix", "", "write Kubernetes Secrets <prefix>-ca and <prefix>-<service> (empty: files only)")
		namespace    = fs.String("k8s-namespace", "", "namespace for those Secrets (default: the pod's own)")
		apiServer    = fs.String("k8s-api", "", "API server https:// URL (default: from KUBERNETES_SERVICE_HOST/PORT)")
		force        = fs.Bool("k8s-force", false, "overwrite existing per-service Secrets (never the CA Secret)")
		services     stringSlice
		extraDNS     stringSlice
		extraIPs     stringSlice
	)

	fs.Var(&services, "service", "service name[:CN] pair (repeatable, default: api,launch,runner)")
	fs.Var(&extraDNS, "san-dns", "additional DNS SAN for all service certs (repeatable)")
	fs.Var(&extraIPs, "san-ip", "additional IP SAN for all service certs (repeatable)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return genOpts{}, err
		}
		return genOpts{}, fmt.Errorf("%w: %v", errFlagParse, err)
	}

	if (*caCertFile == "") != (*caKeyFile == "") {
		return genOpts{}, errors.New("-ca-cert and -ca-key must be provided together")
	}

	// Default services if none specified.
	if len(services) == 0 {
		services = stringSlice{"api", "launch", "runner"}
	}

	// Parse service specs.
	specs := make([]serviceSpec, 0, len(services))
	for _, s := range services {
		parts := strings.SplitN(s, ":", 2)
		name := parts[0]
		cn := name
		if len(parts) == 2 {
			cn = parts[1]
		}
		if name == "" {
			return genOpts{}, fmt.Errorf("invalid -service %q: name is empty", s)
		}
		specs = append(specs, serviceSpec{Name: name, CN: cn})
	}

	// Parse extra IPs.
	var parsedIPs []net.IP
	for _, ip := range extraIPs {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return genOpts{}, fmt.Errorf("invalid IP address: %s", ip)
		}
		parsedIPs = append(parsedIPs, parsed)
	}

	// -out only defaults to a directory when nothing else would receive the
	// material; asking for Secrets and getting a surprise directory of private
	// keys in the container is not what the chart's Job wants.
	dir := *outDir
	if dir == "" && *secretPrefix == "" {
		dir = defaultOutDir
	}

	if *secretPrefix != "" {
		if err := validateSecretNames(*secretPrefix, specs); err != nil {
			return genOpts{}, err
		}
	}

	return genOpts{
		OutDir:          dir,
		CACertFile:      *caCertFile,
		CAKeyFile:       *caKeyFile,
		CACN:            *caCN,
		CAOrg:           *caOrg,
		CAValidity:      time.Duration(*caValidity) * 24 * time.Hour,
		CertValidity:    time.Duration(*certValidity) * 24 * time.Hour,
		Services:        specs,
		ExtraDNS:        extraDNS,
		ExtraIPs:        parsedIPs,
		K8sSecretPrefix: *secretPrefix,
		K8sNamespace:    *namespace,
		K8sAPI:          *apiServer,
		K8sForce:        *force,
	}, nil
}

func run(opts genOpts) error {
	ctx := context.Background()

	var client *k8sClient
	if opts.K8sSecretPrefix != "" {
		// Re-checked here, not only in parseFlags, so the names are validated
		// whatever built the options — and so the failure is a message about a
		// flag rather than a 422 after the CA has already been minted.
		if err := validateSecretNames(opts.K8sSecretPrefix, opts.Services); err != nil {
			return err
		}
		var err error
		client, err = newK8sClient(opts.K8sAPI, opts.K8sNamespace)
		if err != nil {
			return fmt.Errorf("kubernetes client: %w", err)
		}
	}

	if opts.OutDir != "" {
		if err := os.MkdirAll(opts.OutDir, 0o750); err != nil {
			return err
		}
	}

	var (
		caCert      *x509.Certificate
		caKey       *ecdsa.PrivateKey
		caFromFiles bool
		err         error
	)

	switch {
	case opts.CACertFile != "":
		caCert, caKey, err = loadCA(opts.CACertFile, opts.CAKeyFile)
		if err != nil {
			return fmt.Errorf("load CA: %w", err)
		}
		caFromFiles = true
		fmt.Printf("CA: loaded from %s (CN=%s)\n", opts.CACertFile, caCert.Subject.CommonName)
		if client != nil {
			// A pinned CA is deliberately not copied into <prefix>-ca — its
			// private key belongs wherever the operator keeps it, not in a
			// Secret we created. The consequence has to be said out loud: with
			// no <prefix>-ca to adopt, a later run that omits -ca-cert mints a
			// fresh root. That run now fails on the first per-service Secret
			// rather than quietly splitting the mesh, but it still fails.
			fmt.Fprintf(os.Stderr, "warning: -ca-cert pins a CA that is not stored in %s; every later run must pass -ca-cert too\n", caSecretName(opts.K8sSecretPrefix))
		}

	case client != nil:
		caCert, caKey, err = ensureCASecret(ctx, client, opts)
		if err != nil {
			return err
		}

	default:
		caCert, caKey, err = generateCA(opts)
		if err != nil {
			return err
		}
		fmt.Printf("CA: %s (%s)\n", opts.CACN, opts.CAOrg)
	}

	caCertPEM := encodePEM("CERTIFICATE", caCert.Raw)

	// A CA loaded from -ca-cert is already on disk and is deliberately not
	// copied into -out; anything we minted or read back from the CA Secret is
	// written there when the operator asked for files.
	if !caFromFiles && opts.OutDir != "" {
		if err := writeCAFiles(opts.OutDir, caCertPEM, caKey); err != nil {
			return err
		}
		fmt.Printf("  Written to %s/ca.pem (validity: %d days)\n", opts.OutDir, int(caCert.NotAfter.Sub(caCert.NotBefore).Hours()/24))
	}

	for _, svc := range opts.Services {
		certPEM, keyPEM, err := generateServiceCert(opts, svc, caCert, caKey)
		if err != nil {
			return fmt.Errorf("generate %s cert: %w", svc.Name, err)
		}

		if opts.OutDir != "" {
			if err := writeServiceFiles(opts.OutDir, svc.Name, certPEM, keyPEM); err != nil {
				return fmt.Errorf("write %s cert: %w", svc.Name, err)
			}
			fmt.Printf("  %s (CN=%s) written to %s/%s/\n", svc.Name, svc.CN, opts.OutDir, svc.Name)
		}

		if client != nil {
			// ca.pem rides along with every leaf because each service must
			// verify its peers. The CA private key never does — see the package
			// comment; that is the line that keeps one compromised service from
			// minting an identity for every other one.
			name := serviceSecretName(opts.K8sSecretPrefix, svc.Name)
			action, err := client.applyServiceSecret(ctx, name, map[string][]byte{
				keyCACert:  caCertPEM,
				keyTLSCert: certPEM,
				keyTLSKey:  keyPEM,
			}, caCert, opts.K8sForce)
			if err != nil {
				return fmt.Errorf("secret %s: %w", name, err)
			}
			fmt.Printf("  %s (CN=%s) secret %s: %s\n", svc.Name, svc.CN, name, action)
		}
	}

	fmt.Printf("\nDone. Service certificates valid for %d days.\n", int(opts.CertValidity.Hours()/24))
	return nil
}

// generateCA mints a new CA in memory. It writes nothing: the caller decides
// whether the material belongs in files, in a Secret, or in both.
func generateCA(opts genOpts) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	caSerial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization:       []string{opts.CAOrg},
			OrganizationalUnit: []string{"Platform"},
			CommonName:         opts.CACN,
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(opts.CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	return caCert, caKey, nil
}

// writeCAFiles puts the CA certificate and its private key in the output
// directory, 0600. Both files, because that is what -out has always produced
// for a locally minted CA and what the dev tooling reads back with -ca-cert.
func writeCAFiles(outDir string, caCertPEM []byte, caKey *ecdsa.PrivateKey) error {
	if err := writePEMFile(filepath.Join(outDir, "ca.pem"), caCertPEM); err != nil {
		return err
	}

	caKeyPEM, err := encodeECPrivateKey(caKey)
	if err != nil {
		return err
	}
	return writePEMFile(filepath.Join(outDir, "ca-key.pem"), caKeyPEM)
}

func loadCA(certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certFile) // #nosec G304 — CLI tool, paths from trusted args
	if err != nil {
		return nil, nil, fmt.Errorf("read CA cert: %w", err)
	}

	keyPEM, err := os.ReadFile(keyFile) // #nosec G304 — CLI tool, paths from trusted args
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}

	return parseCA(certPEM, keyPEM)
}

// parseCA turns a CA certificate and key held as PEM into usable material,
// wherever they came from — the -ca-cert files or the CA Secret.
//
// It insists the two match. A cert and key that are not a pair still parse
// happily, and every leaf minted from them would fail verification against the
// ca.pem the services were handed — a failure that would only surface at the
// first handshake, in a pod that has already started.
func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, errors.New("CA certificate: no PEM block found")
	}

	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}
	if !caCert.IsCA {
		return nil, nil, fmt.Errorf("CA certificate CN=%s is not a CA certificate", caCert.Subject.CommonName)
	}

	caKey, err := parseECPrivateKey(keyPEM)
	if err != nil {
		return nil, nil, err
	}

	if !caKey.PublicKey.Equal(caCert.PublicKey) {
		return nil, nil, errors.New("CA private key does not match the CA certificate")
	}

	return caCert, caKey, nil
}

// parseECPrivateKey accepts both encodings of an EC key: SEC1 ("EC PRIVATE
// KEY", what this tool writes) and PKCS#8 ("PRIVATE KEY", what openssl and
// cert-manager write by default). Refusing PKCS#8 would fail an install over a
// CA that is cryptographically perfectly fine.
func parseECPrivateKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("CA private key: no PEM block found")
	}

	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: not a SEC1 or PKCS#8 private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse CA key: want an EC private key, got %T", parsed)
	}
	return key, nil
}

// generateServiceCert mints one leaf certificate and returns it as PEM. Like
// generateCA it writes nothing; the same bytes go to a file, a Secret, or both.
func generateServiceCert(opts genOpts, svc serviceSpec, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	// Base SANs: localhost + service-specific names.
	dnsNames := []string{
		"localhost",
		svc.CN,
		svc.Name + ".flomation.local",
	}
	// Add CN as DNS SAN if it differs from the service name.
	if svc.CN != svc.Name {
		dnsNames = append(dnsNames, svc.Name)
	}
	dnsNames = append(dnsNames, opts.ExtraDNS...)

	ips := []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}
	ips = append(ips, opts.ExtraIPs...)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{opts.CAOrg},
			OrganizationalUnit: []string{svc.Name},
			CommonName:         svc.CN,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(opts.CertValidity),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		DNSNames:    dnsNames,
		IPAddresses: ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err = encodeECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	return encodePEM("CERTIFICATE", certDER), keyPEM, nil
}

func writeServiceFiles(outDir, service string, certPEM, keyPEM []byte) error {
	svcDir := filepath.Join(outDir, service)
	if err := os.MkdirAll(svcDir, 0o750); err != nil {
		return err
	}

	if err := writePEMFile(filepath.Join(svcDir, "cert.pem"), certPEM); err != nil {
		return err
	}
	return writePEMFile(filepath.Join(svcDir, "key.pem"), keyPEM)
}

func encodePEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

func encodeECPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal EC private key: %w", err)
	}
	return encodePEM("EC PRIVATE KEY", der), nil
}

// writePEMFile writes PEM bytes 0600 — private keys go through here too, so the
// mode is not negotiable.
func writePEMFile(path string, pemBytes []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 — CLI tool, path derived from -out
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(pemBytes); err != nil {
		return err
	}
	return f.Close()
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
