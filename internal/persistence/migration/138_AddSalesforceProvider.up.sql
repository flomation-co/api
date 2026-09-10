-- Salesforce managed-auth provider — the "Connect Salesforce" flow.
--
-- Without this the Salesforce node is close to unusable: a pasted access token
-- dies at the org's session timeout (commonly ~2 hours), so a flow built in the
-- morning stops working by lunchtime and reads as a Flomation defect rather
-- than an expiring credential. `refresh_token offline_access` yields the
-- refresh token the credential_refresh poller needs to keep it alive.
--
-- The login host is templated on {domain}, an OPTIONAL url_variable defaulting
-- to `login`:
--
--   login.salesforce.com  — production and Developer Edition orgs
--   test.salesforce.com   — every sandbox
--
-- Both are single DNS labels, so they satisfy api.urlVarValuePattern (which
-- restricts a variable value to one label precisely so a crafted value can
-- never rewrite the fixed salesforce.com host). That is also the limitation
-- worth knowing: an org that logs in via its own My Domain host
-- (mycompany.my.salesforce.com) is TWO labels and cannot be expressed here.
-- Those orgs still work — Salesforce accepts login.salesforce.com for them —
-- but an org with "prevent login from login.salesforce.com" enabled needs a
-- second provider row or a relaxed validator. Documented as a follow-up, not a
-- v1 blocker.
--
-- Scopes: `api` is the REST access itself; `id profile email` identify the
-- connected user so the callback can capture the org's instance URL.
--
-- Flomation's own client id/secret come from config.OAuth["salesforce"] via
-- getDefaultClientCredentials — never stored here. Until that config is
-- populated the provider reports configured=false and the editor renders the
-- bring-your-own-app path, which is the intended interim state.
--
-- Register the app as an External Client App (not a legacy Connected App) with
-- PKCE enabled, and list this API's /api/v1/credential/callback as an allowed
-- redirect URL.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes, url_variables) VALUES
    ('salesforce', 'Salesforce', 'salesforce',
     'https://{domain}.salesforce.com/services/oauth2/authorize',
     'https://{domain}.salesforce.com/services/oauth2/token',
     'https://{domain}.salesforce.com/services/oauth2/revoke',
     'api refresh_token offline_access id profile email',
     '[{"key":"domain","label":"Salesforce login host (advanced)","placeholder":"login for production, test for a sandbox — leave blank for production","optional":true,"default":"login"}]')
ON CONFLICT (slug) DO NOTHING;
