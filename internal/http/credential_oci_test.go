package http

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"io"
	"strings"
	"testing"
)

// A fixed public key and its OCI fingerprint as computed by OpenSSL
// (`openssl rsa -pubout -outform DER | openssl md5 -c`) — the definitive
// ground truth for the OCI API-key fingerprint spec.
const goldenPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAyQ4zxjXxVoncx0Jh+I+8
FeZbEMrpTDLCi74tTaIsu2T0sdDxdWKjBMoLVC8QxK4fHyqYMshFg+t7ImXSoom/
ej60raRtUMjTxJTe9Qbt2UjpAvLhuQvdSdsHr4dT8ypGaaGJZC3MsV2119Fuf+cg
IA4yLOjRgwOCj91RWuEmPVnVxykmXHXyQ6zI/RV1Kizuc0HKmsSAUY/vsgEMl34P
qS5WfAcOm4GQIyjkNTnRJ7OJNvgTO1TR6zQVxlkM7trH//f+5CwGfDTVct014/bk
e4DiekGrztZPe39FEqVlD8dUXnoveHOQQaS4RuogYo5Qi0aLgmKIqcFHeJu3jCFY
KwIDAQAB
-----END PUBLIC KEY-----`

const goldenFingerprint = "ec:f1:f9:b8:d6:89:8e:80:f9:bd:a2:4b:b5:7a:d2:1d"

func TestOCIKeyFingerprintMatchesOpenSSL(t *testing.T) {
	block, _ := pem.Decode([]byte(goldenPublicKeyPEM))
	if block == nil {
		t.Fatal("could not decode golden PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if got := ociKeyFingerprint(der); got != goldenFingerprint {
		t.Fatalf("fingerprint mismatch:\n got  %s\n want %s", got, goldenFingerprint)
	}
}

func TestGenerateOCIKeyPair(t *testing.T) {
	priv, pub, fp, err := generateOCIKeyPair()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Private key must be a parseable PKCS#8 PEM.
	pb, _ := pem.Decode([]byte(priv))
	if pb == nil || pb.Type != "PRIVATE KEY" {
		t.Fatalf("private key not a PKCS#8 PEM block: %q", firstLine(priv))
	}
	if _, err := x509.ParsePKCS8PrivateKey(pb.Bytes); err != nil {
		t.Fatalf("private key does not parse: %v", err)
	}
	// Public key must be a parseable PKIX PEM.
	pubBlock, _ := pem.Decode([]byte(pub))
	if pubBlock == nil || pubBlock.Type != "PUBLIC KEY" {
		t.Fatalf("public key not a PKIX PEM block: %q", firstLine(pub))
	}
	if _, err := x509.ParsePKIXPublicKey(pubBlock.Bytes); err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	// Fingerprint is 16 colon-separated hex pairs.
	if parts := strings.Split(fp, ":"); len(parts) != 16 {
		t.Fatalf("fingerprint should be 16 octets, got %d: %s", len(parts), fp)
	}
}

func TestRenderOCIStackZip(t *testing.T) {
	zipBytes, err := renderOCIStackZip(goldenPublicKeyPEM, "compartment", "abcd1234efgh5678")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip open: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	main, ok := files["main.tf"]
	if !ok {
		t.Fatal("main.tf missing from stack zip")
	}
	if !strings.Contains(main, "oci_identity_api_key") || !strings.Contains(main, "BEGIN PUBLIC KEY") {
		t.Fatal("main.tf missing the API key resource or the public key")
	}
	if !strings.Contains(main, "flomation-automate-abcd1234") {
		t.Fatal("main.tf missing the per-credential resource suffix")
	}
	if !strings.Contains(main, `variable "scope" { default = "compartment" }`) {
		t.Fatal("main.tf missing the baked scope default")
	}
	// No schema.yaml — RM auto-injects tenancy/region/compartment; a schema risks rejection.
	if _, ok := files["schema.yaml"]; ok {
		t.Fatal("schema.yaml should not be shipped")
	}
}

func TestValidOCID(t *testing.T) {
	cases := []struct {
		ocid, kind string
		want       bool
	}{
		{"ocid1.tenancy.oc1..aaaaaaaabcdef12345", "tenancy", true},
		{"ocid1.user.oc1..aaaaaaaabcdef12345", "user", true},
		{"ocid1.compartment.oc1..aaaaaaaabcdef12345", "", true},
		{"ocid1.user.oc1..aaaaaaaabcdef12345", "tenancy", false}, // wrong kind
		{"not-an-ocid", "", false},
		{"ocid1.tenancy.", "tenancy", false}, // too short
	}
	for _, tc := range cases {
		if got := validOCID(tc.ocid, tc.kind); got != tc.want {
			t.Errorf("validOCID(%q,%q)=%v want %v", tc.ocid, tc.kind, got, tc.want)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
