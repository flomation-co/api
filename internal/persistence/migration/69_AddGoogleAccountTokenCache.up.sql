-- Cache access tokens and track status for Google accounts.
-- Enables proactive refresh instead of on-demand exchange per use.

ALTER TABLE agent_user_google_account
    ADD COLUMN IF NOT EXISTS access_token BYTEA,
    ADD COLUMN IF NOT EXISTS token_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS last_error TEXT;

ALTER TABLE trigger_google_account
    ADD COLUMN IF NOT EXISTS access_token BYTEA,
    ADD COLUMN IF NOT EXISTS token_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS last_error TEXT;
