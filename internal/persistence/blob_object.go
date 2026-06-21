package persistence

// Blob object storage — the persistence layer behind the mTLS-gated
// /api/v1/internal/blob endpoints. Underpins file attachment support
// across Telegram, Slack and any future channel adapter, as well as
// the executor's tool-output tokenisation (large outputs land here
// instead of inflating the LLM's context window).
//
// At-rest encryption. Migration 98 renamed content → content_enc.
// The application calls pgp_sym_encrypt_bytea() on INSERT and
// pgp_sym_decrypt_bytea() on SELECT, using s.config.Database.EncryptionKey.
// This matches how the codebase protects email_address,
// environment_secret.secret_key and the Sentinel MFA secret —
// blobs join the same posture. A wrong key (or tampered ciphertext)
// raises a SQL error rather than returning garbage; the HTTP layer
// surfaces that as a 500 rather than discriminating against clients.
// Key rotation is a separate operational concern: backfilling
// requires the old key to read + the new key to write, which today
// means dumping + re-uploading. See plans/file_attachments.md for
// the rotation story.
//
// Three invariants this file enforces:
//
//   1. org_id is the auth boundary. Every read carries it in the
//      WHERE clause. Cross-org access returns ErrBlobNotFound — never
//      a 403 — so existence is not leakable.
//   2. TTL is purpose-driven, not caller-driven. The map below is the
//      single source of truth; the upload handler does not honour any
//      ttl_seconds field even if the caller sends one.
//   3. Quota is per-org per-day. Enforced via an UPSERT into
//      blob_quota_daily that *atomically* rejects the upload when the
//      new total would exceed the cap.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flomation.app/automate/api"
)

// Public constants the HTTP layer reads to enforce upload limits.
const (
	BlobMaxSizeBytes      = 25 * 1024 * 1024 // 25 MB hard cap, matches CHECK constraint
	BlobHandleByteLen     = 16
	BlobDailyQuotaPerOrg  = 1 * 1024 * 1024 * 1024 // 1 GB per org/owner per day
	BlobPurposeInbound    = "inbound"
	BlobPurposeToolOutput = "tool_output"
	BlobPurposeManual     = "manual"
)

// BlobScope is the discriminated-union auth boundary applied to every
// blob row. Exactly one of OrgID / OwnerID is set. Org-scoped blobs
// are visible to any caller carrying that org_id; owner-scoped blobs
// are visible only to the user that owns them. Personal-mode agents
// and flows (which have no organisation) live here.
//
// At rest the DB enforces "exactly one" via a CHECK constraint on
// both blob_object and blob_quota_daily. In Go we enforce the same
// rule with Valid(): callers MUST verify before touching persistence.
type BlobScope struct {
	OrgID   string
	OwnerID string
}

// Valid returns true when exactly one of OrgID / OwnerID is set.
// A scope that fails Valid() must not reach persistence — it would
// either violate the CHECK constraint (both set) or be rejected by
// the NULL discrimination (neither set).
func (s BlobScope) Valid() bool {
	return (s.OrgID != "") != (s.OwnerID != "")
}

// IsZero reports whether neither scope dimension is set — used at
// the HTTP layer to distinguish "scope missing" from "scope provided
// but invalid" so we can return the right 400 error.
func (s BlobScope) IsZero() bool {
	return s.OrgID == "" && s.OwnerID == ""
}

// Org returns a BlobScope keyed on an organisation. Convenience
// constructor; the zero string would silently produce IsZero() so we
// don't bother checking here — callers do.
func OrgScope(orgID string) BlobScope { return BlobScope{OrgID: orgID} }

// Owner returns a BlobScope keyed on an individual user.
func OwnerScope(ownerID string) BlobScope { return BlobScope{OwnerID: ownerID} }

// TTLs are purpose-driven and server-enforced. Callers do not get
// to override these. See plans/file_attachments.md M0.
var blobTTLByPurpose = map[string]time.Duration{
	BlobPurposeInbound:    30 * 24 * time.Hour,
	BlobPurposeToolOutput: 1 * time.Hour,
	BlobPurposeManual:     30 * 24 * time.Hour,
}

// ErrBlobNotFound is returned for both genuinely-missing handles and
// for handles owned by another organisation. The HTTP layer surfaces
// it as a 404 unconditionally to avoid leaking existence.
var ErrBlobNotFound = errors.New("blob not found")

// ErrBlobQuotaExceeded is returned when an upload would push the
// org's daily byte total above BlobDailyQuotaPerOrg.
var ErrBlobQuotaExceeded = errors.New("blob daily quota exceeded")

// ErrBlobInvalidPurpose is returned when the caller supplies a purpose
// not in the constants above. The CHECK constraint would reject it
// anyway; we fail fast for a clearer error.
var ErrBlobInvalidPurpose = errors.New("invalid blob purpose")

// ErrBlobScopeInvalid is returned when the supplied BlobScope fails
// Valid() — neither dimension set, or both set. Callers should not
// normally encounter this; the HTTP layer rejects malformed headers
// before reaching persistence.
var ErrBlobScopeInvalid = errors.New("invalid blob scope: exactly one of org_id / owner_id required")

// PutBlob persists bytes and returns the handle (16-byte binary).
// The returned sha256 is the digest of the stored content. scope is
// the auth boundary (org XOR owner — see BlobScope); execID is
// optional (NULL for inbound blobs that predate execution creation).
//
// Enforces:
//   - scope.Valid() — exactly one of OrgID / OwnerID must be set
//   - purpose ∈ {inbound, tool_output, manual}
//   - size ≤ BlobMaxSizeBytes (caller should have already rejected
//     larger payloads, but we double-check here)
//   - quota: per-scope daily byte total ≤ BlobDailyQuotaPerOrg
//
// The TTL is selected purely from purpose via blobTTLByPurpose.
func (s *Service) PutBlob(scope BlobScope, content []byte, mime, purpose string, execID *string) (handle []byte, digest []byte, err error) {
	if !scope.Valid() {
		return nil, nil, ErrBlobScopeInvalid
	}
	ttl, ok := blobTTLByPurpose[purpose]
	if !ok {
		return nil, nil, ErrBlobInvalidPurpose
	}
	if len(content) > BlobMaxSizeBytes {
		return nil, nil, fmt.Errorf("blob exceeds %d-byte cap", BlobMaxSizeBytes)
	}

	handle = make([]byte, BlobHandleByteLen)
	if _, err = rand.Read(handle); err != nil {
		return nil, nil, fmt.Errorf("generate handle: %w", err)
	}
	sum := sha256.Sum256(content)
	digest = sum[:]

	expiresAt := time.Now().Add(ttl)

	tx, err := s.conn.Beginx()
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Quota enforcement: UPSERT today's row keyed on the scope, return
	// new total, reject if over cap. ON CONFLICT relies on
	// blob_quota_daily_scope_day_idx (the unique index that treats
	// NULL as "" via COALESCE).
	today := time.Now().UTC().Format("2006-01-02")
	var newTotal int64
	err = tx.Get(&newTotal, `
		INSERT INTO blob_quota_daily (org_id, owner_id, quota_day, bytes_used)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4)
		ON CONFLICT (
			COALESCE(org_id::text,   ''),
			COALESCE(owner_id::text, ''),
			quota_day
		)
		DO UPDATE SET bytes_used = blob_quota_daily.bytes_used + EXCLUDED.bytes_used
		RETURNING bytes_used`,
		scope.OrgID, scope.OwnerID, today, int64(len(content)))
	if err != nil {
		return nil, nil, fmt.Errorf("update quota: %w", err)
	}
	if newTotal > BlobDailyQuotaPerOrg {
		err = ErrBlobQuotaExceeded
		return nil, nil, err
	}

	// content_enc is pgp_sym_encrypt_bytea(content, key). The Postgres
	// extension handles compression + integrity tagging internally, so
	// the encrypted form is meaningfully larger than the input — that
	// extra storage is the cost of at-rest protection (and budgeted
	// for in the 25 MB cap, which applies to plaintext bytes only).
	_, err = tx.Exec(`
		INSERT INTO blob_object (
			handle, mime, size_bytes, sha256, content_enc,
			org_id, owner_id, execution_id, purpose, expires_at
		)
		VALUES (
			$1, $2, $3, $4, pgp_sym_encrypt_bytea($5, $11),
			NULLIF($6, '')::uuid, NULLIF($7, '')::uuid, $8, $9, $10
		)`,
		handle, mime, int64(len(content)), digest, content,
		scope.OrgID, scope.OwnerID, execID, purpose, expiresAt,
		s.config.Database.EncryptionKey)
	if err != nil {
		return nil, nil, fmt.Errorf("insert blob: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit blob: %w", err)
	}
	return handle, digest, nil
}

// scopeWhereClause returns a `(org_id = $N AND owner_id IS NULL)` or
// `(owner_id = $N AND org_id IS NULL)` predicate plus the value to
// bind. Centralising it keeps the four read/write paths from drifting
// out of sync.
//
// The "other-side IS NULL" tail is the load-bearing bit: it stops a
// caller that knows the handle from reading a row scoped to a
// different dimension. Without it, an org-scoped read could
// accidentally surface an owner-scoped blob with the same handle.
func scopeWhereClause(scope BlobScope, placeholder int) (clause string, arg interface{}) {
	if scope.OrgID != "" {
		return fmt.Sprintf("org_id = $%d AND owner_id IS NULL", placeholder), scope.OrgID
	}
	return fmt.Sprintf("owner_id = $%d AND org_id IS NULL", placeholder), scope.OwnerID
}

// GetBlob returns the stored bytes alongside its mime and recorded
// size. Bumps last_accessed_at in a fire-and-forget fashion (any
// error there is logged at the call site but does not affect the
// read). Cross-scope reads (org→owner or owner→org) collapse to
// ErrBlobNotFound — see scopeWhereClause for why.
func (s *Service) GetBlob(scope BlobScope, handle []byte) (content []byte, mime string, size int64, err error) {
	if !scope.Valid() {
		return nil, "", 0, ErrBlobScopeInvalid
	}
	clause, arg := scopeWhereClause(scope, 2)
	var row struct {
		Content []byte `db:"content"`
		Mime    string `db:"mime"`
		Size    int64  `db:"size_bytes"`
	}
	// pgp_sym_decrypt_bytea($3, key) inverts the PutBlob encryption.
	// A wrong key (or tampered ciphertext) raises a SQL error rather
	// than returning garbage — surface that as a generic read failure
	// so the API doesn't leak the discrimination to clients.
	err = s.conn.Get(&row,
		fmt.Sprintf(`SELECT pgp_sym_decrypt_bytea(content_enc, $3) AS content,
			mime, size_bytes
		FROM blob_object
		WHERE handle = $1 AND %s`, clause),
		handle, arg, s.config.Database.EncryptionKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", 0, ErrBlobNotFound
		}
		return nil, "", 0, err
	}

	// Best-effort access bump; ignore errors so reads never fail on
	// this. last_accessed_at is for future "warm blob" TTL extension.
	_, _ = s.conn.Exec(
		`UPDATE blob_object SET last_accessed_at = NOW() WHERE handle = $1`,
		handle)

	return row.Content, row.Mime, row.Size, nil
}

// HeadBlob returns metadata only — used by callers that want to
// verify a blob exists or read its mime/size without paying the I/O
// cost of streaming the bytes.
func (s *Service) HeadBlob(scope BlobScope, handle []byte) (api.BlobMetadata, error) {
	if !scope.Valid() {
		return api.BlobMetadata{}, ErrBlobScopeInvalid
	}
	clause, arg := scopeWhereClause(scope, 2)
	var meta api.BlobMetadata
	err := s.conn.Get(&meta,
		fmt.Sprintf(`SELECT
			encode(handle, 'hex') AS handle_hex,
			mime,
			size_bytes,
			encode(sha256, 'hex') AS sha256_hex,
			purpose,
			created_at,
			expires_at
		FROM blob_object
		WHERE handle = $1 AND %s`, clause),
		handle, arg)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return api.BlobMetadata{}, ErrBlobNotFound
		}
		return api.BlobMetadata{}, err
	}
	return meta, nil
}

// DeleteBlob removes a blob early — used by callers that want to
// clean up after a failed retry. Cross-scope and unknown handles
// return ErrBlobNotFound (no leak).
func (s *Service) DeleteBlob(scope BlobScope, handle []byte) error {
	if !scope.Valid() {
		return ErrBlobScopeInvalid
	}
	clause, arg := scopeWhereClause(scope, 2)
	res, err := s.conn.Exec(
		fmt.Sprintf(`DELETE FROM blob_object WHERE handle = $1 AND %s`, clause),
		handle, arg)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBlobNotFound
	}
	return nil
}

// SweepExpiredBlobs deletes up to `limit` rows whose expires_at has
// passed. Designed for repeated invocation from a background poller
// — returns the count actually deleted so the caller can loop until
// drained.
func (s *Service) SweepExpiredBlobs(limit int) (int64, error) {
	if limit <= 0 {
		limit = 200
	}
	res, err := s.conn.Exec(`
		DELETE FROM blob_object
		WHERE handle IN (
			SELECT handle FROM blob_object
			WHERE expires_at < NOW()
			LIMIT $1
		)`, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SweepStaleBlobQuotaRows trims the per-org per-day quota counter
// table; anything older than 7 days is just clutter.
func (s *Service) SweepStaleBlobQuotaRows() (int64, error) {
	res, err := s.conn.Exec(
		`DELETE FROM blob_quota_daily WHERE quota_day < (CURRENT_DATE - INTERVAL '7 days')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
