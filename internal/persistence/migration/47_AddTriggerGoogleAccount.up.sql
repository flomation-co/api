-- Trigger-scoped Google account connections. These are owned by the
-- trigger itself (not an agent_user), allowing email triggers to work
-- in standalone flows without agent context. Multiple accounts can be
-- connected per trigger, each with its own purpose-scoped token.

CREATE TABLE IF NOT EXISTS trigger_google_account (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id        TEXT NOT NULL,
    google_email      TEXT NOT NULL,
    refresh_token     BYTEA NOT NULL,
    scopes            TEXT,
    label             TEXT,
    purpose           TEXT NOT NULL DEFAULT 'email_read',
    connected_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(trigger_id, google_email, purpose)
);

CREATE INDEX IF NOT EXISTS idx_trigger_google_account_trigger
    ON trigger_google_account(trigger_id);
