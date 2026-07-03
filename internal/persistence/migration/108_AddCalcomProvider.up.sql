-- Cal.com developer-OAuth2 provider (authorization-code redirect flow).
-- Verified live against the account's OAuth client: authorize at
-- https://app.cal.com/auth/oauth2/authorize, token exchange at
-- https://api.cal.com/v2/auth/oauth2/token (form-urlencoded, standard OAuth
-- response {access_token, refresh_token, expires_in, token_type, scope};
-- access tokens live 30 min and refresh via the refresh_token grant — the
-- credential_refresh poller handles that unchanged).
--
-- Cal.com REQUIRES an explicit scope parameter, so default_scopes is set (not
-- NULL like Calendly). These are the user-level scopes the shipped Cal.com
-- nodes use; the connecting user's OAuth client must have these enabled and
-- must list the environment's /api/v1/credential/callback URL as a redirect
-- URI. Team/org scopes (TEAM_*, ORG_*) can be appended per-credential when a
-- flow uses team features.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('calcom', 'Cal.com', 'calcom',
     'https://app.cal.com/auth/oauth2/authorize',
     'https://api.cal.com/v2/auth/oauth2/token',
     NULL,
     'EVENT_TYPE_READ EVENT_TYPE_WRITE BOOKING_READ BOOKING_WRITE SCHEDULE_READ SCHEDULE_WRITE PROFILE_READ WEBHOOK_READ WEBHOOK_WRITE')
ON CONFLICT (slug) DO NOTHING;
