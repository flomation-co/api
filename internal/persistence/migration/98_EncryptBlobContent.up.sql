-- Encrypts blob_object.content at rest with the database encryption
-- key, bringing it to parity with how the API protects other
-- sensitive material (email_address, environment_secret.secret_key,
-- mfa_device.secret in Sentinel, agent_user_google_account.access_token).
--
-- Rationale. M0 stored content as plaintext bytea. Anyone with
-- read on blob_object — DB admin, backup, replica, analyst query —
-- would see the raw file bytes. Voice notes, photos of IDs, OCR
-- output, TTS audio repeating verification codes all flow through
-- here. Same threat model as the existing PGP_SYM_ENCRYPTed columns
-- elsewhere in the schema; same fix.
--
-- Implementation. The application calls pgp_sym_encrypt_bytea() on
-- INSERT and pgp_sym_decrypt_bytea() on SELECT. The column is
-- renamed content_enc so anyone querying the table sees at a glance
-- that the bytes are not directly readable.
--
-- Backfill. Existing blob_object rows are pre-feature local-dev test
-- data only — M0 (which introduced the table) and M2 (which started
-- populating it) are both unmerged at the time this migration was
-- written, so production has nothing here. Truncating is safe in
-- this window. Quota counters are zeroed too: their values reference
-- now-deleted byte counts.

TRUNCATE blob_object;
TRUNCATE blob_quota_daily;

ALTER TABLE blob_object RENAME COLUMN content TO content_enc;

COMMENT ON COLUMN blob_object.content_enc IS
    'pgp_sym_encrypt_bytea(content, database_encryption_key). Application reads via pgp_sym_decrypt_bytea on SELECT. See internal/persistence/blob_object.go.';
