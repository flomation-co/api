package http

import "testing"

// The OCI live-dropdown proxy resolves managed "Connect Oracle Cloud" references so
// pickers work with a connected credential (not only the raw signing-key fields).
// Credential names contain dashes/underscores, so the split must be on the FIRST dot.
func TestParseOCICredentialRef(t *testing.T) {
	cases := []struct{ ref, name, field string }{
		{"${credentials.dm-manual-test}", "dm-manual-test", ""},
		{"${credentials.dm-manual-test.region}", "dm-manual-test", "region"},
		{"${credentials.oracle_cloud.compartment_ocid}", "oracle_cloud", "compartment_ocid"},
		{"${credentials.oracle_cloud.tenancy_ocid}", "oracle_cloud", "tenancy_ocid"},
		{"${credentials.a}", "a", ""},
		{"${credentials.a.fingerprint}", "a", "fingerprint"},
	}
	for _, tc := range cases {
		if n, f := parseOCICredentialRef(tc.ref); n != tc.name || f != tc.field {
			t.Errorf("parseOCICredentialRef(%q) = (%q, %q); want (%q, %q)", tc.ref, n, f, tc.name, tc.field)
		}
	}
}
