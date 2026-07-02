-- Per-tenant OAuth URL templating.
--
-- Some providers (Shopify, and any SaaS with per-account OAuth endpoints)
-- have authorize/token URLs that embed a tenant identifier — e.g. Shopify's
-- https://{shop}.myshopify.com/admin/oauth/authorize. A provider can now
-- declare the URL variables it needs in url_variables; the value the user
-- supplies per credential is stored in environment_credential.metadata under
-- "url_vars" and substituted into the URL at authorize/token/refresh time.
ALTER TABLE credential_provider ADD COLUMN IF NOT EXISTS url_variables JSONB;

-- Shopify — authorization-code OAuth yields a permanent (non-expiring) offline
-- token, so it never enters the refresh poller (which is gated on a non-null
-- expiry). The user supplies the shop subdomain per credential; their app's
-- Client ID/Secret are stored per credential. Scopes are comma-separated as
-- Shopify expects. The app must list this API's /api/v1/credential/callback as
-- an allowed redirect URL.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes, url_variables) VALUES
    ('shopify', 'Shopify', 'shopify',
     'https://{shop}.myshopify.com/admin/oauth/authorize',
     'https://{shop}.myshopify.com/admin/oauth/access_token',
     NULL,
     'read_products,write_products,read_orders,write_orders',
     '[{"key":"shop","label":"Shop Subdomain","placeholder":"my-store (from my-store.myshopify.com)"}]')
ON CONFLICT (slug) DO NOTHING;
