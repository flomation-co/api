-- Reverts migration 102: Microsoft token columns return to plain TEXT.
-- Same TRUNCATE rationale as the up — encrypted bytea bytes can't be
-- read back as TEXT without the encryption key, and there's no
-- production data to preserve in this deploy window.

TRUNCATE microsoft_account;
TRUNCATE trigger_microsoft_account;
TRUNCATE microsoft_auth_state;

ALTER TABLE microsoft_account
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    ADD COLUMN access_token  TEXT NOT NULL,
    ADD COLUMN refresh_token TEXT NOT NULL;

ALTER TABLE trigger_microsoft_account
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    ADD COLUMN access_token  TEXT NOT NULL,
    ADD COLUMN refresh_token TEXT NOT NULL;
