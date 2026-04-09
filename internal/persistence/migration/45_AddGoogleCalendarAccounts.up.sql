-- Google Calendar integration: per-user OAuth tokens.
--
-- Each agent_user can connect multiple Google accounts. Tokens are
-- scoped per-account and encrypted at rest. The agent queries all
-- connected accounts and merges calendar data into a combined view.

CREATE TABLE IF NOT EXISTS agent_user_google_account (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_user_id     UUID NOT NULL REFERENCES agent_user(id) ON DELETE CASCADE,
    google_email      TEXT NOT NULL,
    refresh_token     BYTEA NOT NULL,
    scopes            TEXT,
    label             TEXT,
    connected_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_user_id, google_email)
);

CREATE INDEX IF NOT EXISTS idx_agent_user_google_account_user
    ON agent_user_google_account(agent_user_id);
