-- QuickBooks Online + Xero accounting providers (authorization-code OAuth2).
--
-- Both differ from the earlier providers in TWO ways that the rest of the
-- integration handles (not this migration):
--
--   1. Token-endpoint auth. Intuit's token endpoint REQUIRES HTTP Basic auth
--      (Authorization: Basic base64(client_id:client_secret)) and rejects
--      client credentials in the form body; Xero accepts Basic too. The token
--      exchange (exchangeOAuthCode) and the refresh poller send Basic for
--      these two providers. Xero also ROTATES its refresh_token on every
--      refresh — the poller must persist the new one or the credential dies
--      on the next cycle.
--
--   2. Per-account identifier is discovered AFTER authorisation, so it is NOT
--      a url_variable (which is a pre-auth, user-supplied value substituted
--      into the OAuth URL, like Shopify's shop subdomain). QuickBooks returns
--      realmId as a callback query parameter; Xero requires a follow-up
--      GET https://api.xero.com/connections call. The OAuth callback captures
--      it into environment_credential.metadata ({"realm_id":...} /
--      {"tenant_id":...,"tenant_name":...,"connections":[...]}) and the
--      executor reads it via ${credentials.<name>.realm_id|tenant_id}.
--      url_variables therefore stays NULL for both.
--
-- The connecting app must list this API's /api/v1/credential/callback as an
-- allowed redirect URI. Xero REQUIRES the offline_access scope to be granted
-- a refresh token; QuickBooks issues one by default with the offline
-- access_type the OAuth URL builder already sets.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('quickbooks', 'QuickBooks Online', 'quickbooks',
     'https://appcenter.intuit.com/connect/oauth2',
     'https://oauth.platform.intuit.com/oauth2/v1/tokens/bearer',
     'https://developer.api.intuit.com/v2/oauth2/tokens/revoke',
     'com.intuit.quickbooks.accounting'),
    ('xero', 'Xero', 'xero',
     'https://login.xero.com/identity/connect/authorize',
     'https://identity.xero.com/connect/token',
     'https://identity.xero.com/connect/revocation',
     'openid profile email accounting.transactions accounting.contacts accounting.settings offline_access')
ON CONFLICT (slug) DO NOTHING;
