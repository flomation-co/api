-- OAuth credential management for environments.
-- Credentials store encrypted OAuth tokens that are proactively refreshed.

CREATE TABLE IF NOT EXISTS credential_provider (
    slug            VARCHAR(50) PRIMARY KEY,
    name            VARCHAR(100) NOT NULL,
    icon            VARCHAR(50) NOT NULL DEFAULT 'key',
    auth_url        TEXT NOT NULL,
    token_url       TEXT NOT NULL,
    revoke_url      TEXT,
    default_scopes  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS environment_credential (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id      UUID NOT NULL REFERENCES environment(id) ON DELETE CASCADE,
    provider_slug       VARCHAR(50) NOT NULL REFERENCES credential_provider(slug),
    name                VARCHAR(100) NOT NULL,
    client_id           BYTEA,
    client_secret       BYTEA,
    access_token        BYTEA,
    refresh_token       BYTEA,
    token_expires_at    TIMESTAMPTZ,
    scopes              TEXT,
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
    last_refreshed_at   TIMESTAMPTZ,
    last_error          TEXT,
    metadata            JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(environment_id, name)
);

CREATE INDEX IF NOT EXISTS idx_credential_environment ON environment_credential(environment_id);
CREATE INDEX IF NOT EXISTS idx_credential_status ON environment_credential(status);
CREATE INDEX IF NOT EXISTS idx_credential_expires ON environment_credential(token_expires_at) WHERE status = 'active';

-- Seed initial OAuth providers
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('google', 'Google', 'google', 'https://accounts.google.com/o/oauth2/v2/auth', 'https://oauth2.googleapis.com/token', 'https://oauth2.googleapis.com/revoke', 'openid email profile'),
    ('microsoft', 'Microsoft', 'microsoft', 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize', 'https://login.microsoftonline.com/common/oauth2/v2.0/token', NULL, 'openid email profile offline_access'),
    ('github', 'GitHub', 'github', 'https://github.com/login/oauth/authorize', 'https://github.com/login/oauth/access_token', NULL, 'read:user user:email'),
    ('linkedin', 'LinkedIn', 'linkedin', 'https://www.linkedin.com/oauth/v2/authorization', 'https://www.linkedin.com/oauth/v2/accessToken', NULL, 'openid profile email w_member_social'),
    ('linkedin_community', 'LinkedIn Community', 'linkedin', 'https://www.linkedin.com/oauth/v2/authorization', 'https://www.linkedin.com/oauth/v2/accessToken', NULL, 'r_member_social w_member_social r_organization_social w_organization_social rw_organization_admin'),
    ('facebook', 'Facebook', 'facebook', 'https://www.facebook.com/v19.0/dialog/oauth', 'https://graph.facebook.com/v19.0/oauth/access_token', NULL, 'pages_manage_posts pages_read_engagement'),
    ('twitter', 'X / Twitter', 'twitter', 'https://twitter.com/i/oauth2/authorize', 'https://api.twitter.com/2/oauth2/token', 'https://api.twitter.com/2/oauth2/revoke', 'tweet.read tweet.write users.read offline.access')
ON CONFLICT (slug) DO NOTHING;
