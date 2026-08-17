package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags("gencerts", nil, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if opts.OutDir != defaultOutDir {
		t.Errorf("OutDir = %q, want %q", opts.OutDir, defaultOutDir)
	}
	if opts.CACN != "Flomation Dev CA" || opts.CAOrg != "Flomation" {
		t.Errorf("CA identity = %q/%q", opts.CACN, opts.CAOrg)
	}
	if opts.CAValidity != 3650*24*time.Hour {
		t.Errorf("CAValidity = %v, want 3650 days", opts.CAValidity)
	}
	if opts.CertValidity != 365*24*time.Hour {
		t.Errorf("CertValidity = %v, want 365 days", opts.CertValidity)
	}

	want := []serviceSpec{{Name: "api", CN: "api"}, {Name: "launch", CN: "launch"}, {Name: "runner", CN: "runner"}}
	if len(opts.Services) != len(want) {
		t.Fatalf("Services = %v, want %v", opts.Services, want)
	}
	for i, w := range want {
		if opts.Services[i] != w {
			t.Errorf("Services[%d] = %v, want %v", i, opts.Services[i], w)
		}
	}

	// Secret output is opt-in: the default run is exactly today's behaviour,
	// files only, no API server contacted.
	if opts.K8sSecretPrefix != "" || opts.K8sNamespace != "" || opts.K8sAPI != "" || opts.K8sForce {
		t.Errorf("k8s defaults are not inert: %+v", opts)
	}
}

// -k8s-secret-prefix without -out writes no files. The chart's Job has a
// read-only root filesystem; a surprise MkdirAll there is a failed install.
func TestParseFlagsOutDirIsOptionalWithSecrets(t *testing.T) {
	opts, err := parseFlags("gencerts", []string{"-k8s-secret-prefix", "flo"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.OutDir != "" {
		t.Errorf("OutDir = %q, want no file output", opts.OutDir)
	}
	if opts.K8sSecretPrefix != "flo" {
		t.Errorf("K8sSecretPrefix = %q", opts.K8sSecretPrefix)
	}

	// Both together still writes both.
	opts, err = parseFlags("gencerts", []string{"-k8s-secret-prefix", "flo", "-out", "/tmp/certs"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.OutDir != "/tmp/certs" {
		t.Errorf("OutDir = %q, want /tmp/certs", opts.OutDir)
	}
}

func TestParseFlagsValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"ca cert without key", []string{"-ca-cert", "ca.pem"}, "must be provided together"},
		{"ca key without cert", []string{"-ca-key", "ca-key.pem"}, "must be provided together"},
		{"bad ip", []string{"-san-ip", "not-an-ip"}, "invalid IP address"},
		{"bad secret prefix", []string{"-k8s-secret-prefix", "Flo_Mation"}, "not a valid Kubernetes name"},
		{"bad service name", []string{"-k8s-secret-prefix", "flo", "-service", "API"}, "not a valid Kubernetes name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags("gencerts", tc.args, io.Discard)
			if err == nil {
				t.Fatalf("args %v were accepted", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	// An unknown flag is reported by the flag package itself; main must not
	// print it twice.
	if _, err := parseFlags("gencerts", []string{"-nope"}, io.Discard); !errors.Is(err, errFlagParse) {
		t.Fatalf("unknown flag error = %v, want errFlagParse", err)
	}
	if _, err := parseFlags("gencerts", []string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h error = %v, want flag.ErrHelp", err)
	}
}

func TestParseFlagsServiceCNPair(t *testing.T) {
	opts, err := parseFlags("gencerts", []string{"-service", "api:api.flomation.svc", "-service", "runner"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := []serviceSpec{{Name: "api", CN: "api.flomation.svc"}, {Name: "runner", CN: "runner"}}
	for i, w := range want {
		if opts.Services[i] != w {
			t.Errorf("Services[%d] = %v, want %v", i, opts.Services[i], w)
		}
	}
}

// File output is unchanged by the Secret work: same paths, same 0600, same
// chain.
func TestFileOutputUnchanged(t *testing.T) {
	dir := t.TempDir()
	opts, err := parseFlags("gencerts", []string{"-out", dir}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	caPEM := readFile(t, filepath.Join(dir, "ca.pem"))
	if _, _, err := parseCA(caPEM, readFile(t, filepath.Join(dir, "ca-key.pem"))); err != nil {
		t.Fatalf("CA files do not form a usable CA: %v", err)
	}

	for _, svc := range []string{"api", "launch", "runner"} {
		certPath := filepath.Join(dir, svc, "cert.pem")
		keyPath := filepath.Join(dir, svc, "key.pem")
		verifyLeaf(t, caPEM, readFile(t, certPath))

		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat %s: %v", keyPath, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", keyPath, perm)
		}
	}
}

// -ca-cert still pins an existing CA, and still does not copy it into -out.
func TestExistingCAFilesArePinned(t *testing.T) {
	src := t.TempDir()
	caCert, caKey, err := generateCA(genOpts{CACN: "Pinned CA", CAOrg: "Flomation", CAValidity: 3650 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	caCertPEM := encodePEM("CERTIFICATE", caCert.Raw)
	caKeyPEM, err := encodeECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("encode CA key: %v", err)
	}
	certFile := filepath.Join(src, "ca.pem")
	keyFile := filepath.Join(src, "ca-key.pem")
	if err := writePEMFile(certFile, caCertPEM); err != nil {
		t.Fatal(err)
	}
	if err := writePEMFile(keyFile, caKeyPEM); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	opts, err := parseFlags("gencerts", []string{"-out", out, "-ca-cert", certFile, "-ca-key", keyFile}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	verifyLeaf(t, caCertPEM, readFile(t, filepath.Join(out, "api", "cert.pem")))
	if _, err := os.Stat(filepath.Join(out, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatal("a pinned CA key was copied into the output directory")
	}
}

// The SANs a service certificate carries are load-bearing for mTLS: -san-dns
// and -san-ip must survive onto every leaf.
func TestExtraSANsReachTheLeaf(t *testing.T) {
	dir := t.TempDir()
	opts, err := parseFlags("gencerts", []string{
		"-out", dir,
		"-service", "api",
		"-san-dns", "flo-api.flomation.svc.cluster.local",
		"-san-ip", "10.0.0.5",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	block, _ := pem.Decode(readFile(t, filepath.Join(dir, "api", "cert.pem")))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if err := leaf.VerifyHostname("flo-api.flomation.svc.cluster.local"); err != nil {
		t.Errorf("-san-dns did not reach the leaf: %v", err)
	}
	if err := leaf.VerifyHostname("10.0.0.5"); err != nil {
		t.Errorf("-san-ip did not reach the leaf: %v", err)
	}
}

// A cancelled run must never report success. The chart's Job gates on the exit
// code, so a Job that was terminated part-way through provisioning the trust
// root and still exited 0 is the exact failure this tool exists to prevent.
//
// The file path is the one worth pinning: it makes no API calls, so nothing in
// it would otherwise notice the context at all.
func TestCancelledRunNeverReportsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts, err := parseFlags("gencerts", []string{"-out", t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	err = run(ctx, opts)
	if err == nil {
		t.Fatal("a cancelled run exited 0; the Job would report success over certificates it never finished writing")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error is not identifiable as a cancellation, so main cannot report it as one: %v", err)
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("error does not say it was cancelled: %v", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 — test path from t.TempDir
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
