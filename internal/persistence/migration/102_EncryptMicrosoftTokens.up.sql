-- Brings the Microsoft account tables into parity with the Google
-- equivalents on encryption-at-rest. Migration 78 created
-- microsoft_account / trigger_microsoft_account with access_token and
-- refresh_token stored as plain TEXT. Anyone with SELECT on those
-- tables (DB admin, backup, replica, analyst) would see the raw
-- tokens — same threat model agent_user_google_account.refresh_token
-- protects against via PGP_SYM_ENCRYPT.
--
-- Implementation. Columns move from TEXT to BYTEA. The application
-- calls PGP_SYM_ENCRYPT(value, database_encryption_key) on INSERT/
-- UPDATE and PGP_SYM_DECRYPT(column, database_encryption_key) on
-- SELECT, exactly the same pattern as google_accounts.go.
--
-- Backfill. The Microsoft tables are pre-feature: no Go code in the
-- API or Launch reads or writes them yet (the parallel persistence
-- helper for the cross-channel widening landed in the same PR that
-- introduces this migration). Any rows present are dev/test residue
-- — TRUNCATE is safe in this deploy window. microsoft_auth_state is
-- a 10-minute transient OAuth state cache; truncating it kicks any
-- in-flight grants but those would have expired anyway.

TRUNCATE microsoft_account;
TRUNCATE trigger_microsoft_account;
TRUNCATE microsoft_auth_state;

ALTER TABLE microsoft_account
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    ADD COLUMN access_token  BYTEA NOT NULL,
    ADD COLUMN refresh_token BYTEA NOT NULL;

ALTER TABLE trigger_microsoft_account
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    ADD COLUMN access_token  BYTEA NOT NULL,
    ADD COLUMN refresh_token BYTEA NOT NULL;

COMMENT ON COLUMN microsoft_account.access_token IS
    'PGP_SYM_ENCRYPT(access_token, database_encryption_key). Read via PGP_SYM_DECRYPT on SELECT — see internal/persistence/microsoft_accounts.go.';
COMMENT ON COLUMN microsoft_account.refresh_token IS
    'PGP_SYM_ENCRYPT(refresh_token, database_encryption_key). Read via PGP_SYM_DECRYPT on SELECT — see internal/persistence/microsoft_accounts.go.';
COMMENT ON COLUMN trigger_microsoft_account.access_token IS
    'PGP_SYM_ENCRYPT(access_token, database_encryption_key). Read via PGP_SYM_DECRYPT on SELECT.';
COMMENT ON COLUMN trigger_microsoft_account.refresh_token IS
    'PGP_SYM_ENCRYPT(refresh_token, database_encryption_key). Read via PGP_SYM_DECRYPT on SELECT.';
