-- Calendly OAuth2 provider. Fixed endpoints (no per-tenant URL variables) and
-- no scope parameter — Calendly grants a single default scope per app.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('calendly', 'Calendly', 'calendly',
     'https://auth.calendly.com/oauth/authorize',
     'https://auth.calendly.com/oauth/token',
     'https://auth.calendly.com/oauth/revoke',
     NULL)
ON CONFLICT (slug) DO NOTHING;
