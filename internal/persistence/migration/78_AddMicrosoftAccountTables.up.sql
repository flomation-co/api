-- Microsoft account connections for agent-users (parallel to google_account)
CREATE TABLE IF NOT EXISTS microsoft_account (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_user_id    UUID NOT NULL,
    email            VARCHAR(255) NOT NULL,
    label            VARCHAR(50),
    purpose          VARCHAR(50) NOT NULL DEFAULT 'mail_read',
    access_token     TEXT NOT NULL,
    refresh_token    TEXT NOT NULL,
    token_expires_at TIMESTAMPTZ,
    status           VARCHAR(20) NOT NULL DEFAULT 'active',
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_microsoft_account_agent_user
    ON microsoft_account (agent_user_id, purpose);

-- OAuth state for Microsoft account linking flow
CREATE TABLE IF NOT EXISTS microsoft_auth_state (
    state         VARCHAR(128) PRIMARY KEY,
    agent_id      UUID,
    agent_user_id UUID,
    trigger_id    UUID,
    purpose       VARCHAR(50) NOT NULL DEFAULT 'mail_read',
    expires_at    TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 minutes'),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Trigger-scoped Microsoft accounts
CREATE TABLE IF NOT EXISTS trigger_microsoft_account (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id       UUID NOT NULL,
    email            VARCHAR(255) NOT NULL,
    label            VARCHAR(50),
    purpose          VARCHAR(50) NOT NULL DEFAULT 'mail_read',
    access_token     TEXT NOT NULL,
    refresh_token    TEXT NOT NULL,
    token_expires_at TIMESTAMPTZ,
    status           VARCHAR(20) NOT NULL DEFAULT 'active',
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_trigger_microsoft_account_trigger
    ON trigger_microsoft_account (trigger_id, purpose);
