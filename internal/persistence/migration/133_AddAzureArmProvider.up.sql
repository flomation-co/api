-- Azure Resource Manager (ARM) managed-auth provider — Phase 1 of "Connect
-- Azure".
--
-- Delegated OAuth against the management plane: the customer consents once, the
-- generic credential connector mints + auto-refreshes the token, and the Azure
-- compute nodes authenticate with that managed bearer instead of a pasted
-- service-principal secret. `offline_access` yields the refresh token the
-- credential_refresh poller needs; `user_impersonation` is the delegated ARM
-- scope. Flomation's own multi-tenant app client id/secret come from
-- config.OAuth["azure-arm"] via getDefaultClientCredentials — not stored here.
--
-- The authority is templated on {tenant} (an OPTIONAL url_variable, default
-- `organizations`). `organizations` is correct for the common case — a customer
-- signing in with an Entra work/school account resolves to their own tenant and
-- gets an ARM token for it. Two cases need to override it: (1) guest / MSP /
-- cross-tenant users, who at `organizations` authenticate into their HOME tenant
-- and would get a token that can't see the resource tenant's subscription; and
-- (2) personal-Microsoft-account members, whom `organizations` rejects outright.
-- Both supply their tenant ID (a GUID) to pin the authority to that tenant. The
-- value is a single DNS/path label (letters, digits, hyphens — GUID-shaped), so
-- it can never rewrite the fixed login.microsoftonline.com host.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes, url_variables) VALUES
    ('azure-arm', 'Azure', 'azure',
     'https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize',
     'https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token',
     NULL,
     'https://management.azure.com/user_impersonation offline_access',
     '[{"key":"tenant","label":"Azure tenant (advanced)","placeholder":"Tenant ID (GUID) — leave blank to sign in to any organisation","optional":true,"default":"organizations"}]')
ON CONFLICT (slug) DO NOTHING;
