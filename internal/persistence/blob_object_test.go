package persistence

// Lightweight tests for the blob_object package's pure-function
// surface. The HTTP-layer tests in internal/http/blob_test.go cover
// the end-to-end multipart upload + cross-org + quota branches via
// a stubbed Persistence; this file pins down the constants and the
// TTL map that the migration's CHECK constraint mirrors.
//
// We deliberately don't spin up a real Postgres here — the
// persistence functions are thin SQL wrappers and any meaningful
// test would be re-asserting the migration's invariants. Where the
// migration says CHECK (size_bytes <= 26214400), we assert that the
// Go-side constant matches.

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// TestBlobMaxSizeBytes_MatchesMigration guards the migration's CHECK
// constraint against drift. If somebody bumps the constant in Go but
// forgets the migration, the persistence layer would accept rows that
// the database then rejects with a constraint violation — a confusing
// failure mode. This test forces the two values to stay aligned.
func TestBlobMaxSizeBytes_MatchesMigration(t *testing.T) {
	RegisterTestingT(t)
	Expect(BlobMaxSizeBytes).To(Equal(26214400))
}

// TestBlobHandleByteLen is load-bearing for the wire format. The hex
// representation is exactly 32 characters, and the executor's
// ParseBlobToken expects that length. Pinning the constant here means
// any drift fails fast in unit tests instead of silently in
// production round-trips.
func TestBlobHandleByteLen_Is16(t *testing.T) {
	RegisterTestingT(t)
	Expect(BlobHandleByteLen).To(Equal(16))
}

// TestBlobTTLs_PurposeDrivenAndCorrect verifies the TTL map has all
// three valid purposes wired up with the documented durations. The
// HTTP handler delegates the choice to this map — a missing key would
// surface as ErrBlobInvalidPurpose, but a wrong duration would only
// be caught at GC time. Cheap to assert here.
func TestBlobTTLs_PurposeDrivenAndCorrect(t *testing.T) {
	RegisterTestingT(t)
	Expect(blobTTLByPurpose).To(HaveKeyWithValue(BlobPurposeInbound, 30*24*time.Hour))
	Expect(blobTTLByPurpose).To(HaveKeyWithValue(BlobPurposeToolOutput, 1*time.Hour))
	Expect(blobTTLByPurpose).To(HaveKeyWithValue(BlobPurposeManual, 30*24*time.Hour))
	// Assets are permanent: a 0 duration signals "no expiry" (expires_at NULL).
	Expect(blobTTLByPurpose).To(HaveKeyWithValue(BlobPurposeAsset, time.Duration(0)))
	Expect(blobTTLByPurpose).To(HaveLen(4))
}

// TestBlobDailyQuotaPerOrg_IsOneGigabyte ensures the per-org daily
// quota stays at the documented 1 GB. Bumping it requires an
// intentional change to this test, prompting a thought about whether
// the storage tier (still in-table Postgres) can absorb the increase.
func TestBlobDailyQuotaPerOrg_IsOneGigabyte(t *testing.T) {
	RegisterTestingT(t)
	// Constant is untyped — compare via int so the assertion matches
	// the declaration's compile-time form. The quota promotes to
	// int64 at use sites (cf. PutBlob's newTotal comparison).
	Expect(BlobDailyQuotaPerOrg).To(Equal(1 * 1024 * 1024 * 1024))
}
