-- Google Ads managed-auth provider — the "Connect Google Ads" flow.
--
-- A SEPARATE row from the existing `google` provider (migration 67), not a
-- widening of it. The scope here is
-- https://www.googleapis.com/auth/adwords, which carries full spend authority
-- over the customer's advertising; bolting it onto the provider that also
-- serves Drive, Sheets and Calendar would mean anyone connecting a Google
-- account to read a spreadsheet is asked to hand over their ad budget. The
-- `linkedin_community` row (migration 68) exists beside the base LinkedIn one
-- for exactly this reason.
--
-- No developer token is involved. Developer tokens were SUNSET on
-- 9 September 2026: API access level now attaches to the Google Cloud project
-- that owns the OAuth client, Google documents the developer-token header as
-- "optional and ignored by the API servers", and promises to start rejecting it
-- in a future major version. So there is nothing for a customer to apply for
-- and nothing extra to store — the access level is a property of OUR project.
--
-- Access levels, for whoever is watching the quota (all per rolling 24 hours):
--
--   Explorer (default)  2,880 operations/day against LIVE accounts
--   Basic               15,000/day — automated approval after brand verification
--   Standard            unlimited — manual audit plus Required Minimum
--                       Functionality, which a tool serving external users must
--                       satisfy across creation, management AND reporting
--
-- `access_type=offline` and `prompt=consent` are already sent unconditionally by
-- buildOAuthURL, which is what makes Google issue a refresh token at all — no
-- per-provider special case is needed here (unlike ProviderUsesBasicAuth for
-- Intuit, or ProviderUsesPKCE for Salesforce).
--
-- Flomation's own client id/secret come from config.OAuth["google_ads"] via
-- getDefaultClientCredentials and are never stored here. Until that config is
-- populated the provider reports configured=false and the editor renders the
-- bring-your-own-app path, which is the intended interim state.
--
-- Register the OAuth client in the same Cloud project that holds the Google Ads
-- API access level, and list this API's /api/v1/credential/callback as an
-- authorised redirect URI.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('google_ads', 'Google Ads', 'googleads',
     'https://accounts.google.com/o/oauth2/v2/auth',
     'https://oauth2.googleapis.com/token',
     'https://oauth2.googleapis.com/revoke',
     'https://www.googleapis.com/auth/adwords')
ON CONFLICT (slug) DO NOTHING;
