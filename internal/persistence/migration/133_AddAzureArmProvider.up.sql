-- Azure Resource Manager (ARM) managed-auth provider — Phase 1 of "Connect
-- Azure" (see executor/docs/azure-connect-phase1.md).
--
-- Delegated OAuth against the management plane: the customer consents once, the
-- generic credential connector mints + auto-refreshes the token, and the Azure
-- compute nodes authenticate with that managed bearer instead of a pasted
-- service-principal secret. `/organizations` restricts to work/school tenants
-- (no personal Microsoft accounts). `offline_access` yields the refresh token
-- the credential_refresh poller needs; `user_impersonation` is the delegated
-- ARM scope. Flomation's own multi-tenant app client id/secret come from
-- config.OAuth["azure-arm"] via getDefaultClientCredentials — not stored here.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes) VALUES
    ('azure-arm', 'Azure', 'azure',
     'https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize',
     'https://login.microsoftonline.com/organizations/oauth2/v2.0/token',
     NULL,
     'https://management.azure.com/user_impersonation offline_access')
ON CONFLICT (slug) DO NOTHING;
