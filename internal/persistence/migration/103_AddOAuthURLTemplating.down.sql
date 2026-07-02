-- Only remove the seeded provider if no environment still references it — a
-- blind delete would abort on the provider_slug foreign key, and cascading the
-- delete would silently destroy users' stored OAuth tokens.
DELETE FROM credential_provider cp
 WHERE cp.slug = 'shopify'
   AND NOT EXISTS (SELECT 1 FROM environment_credential ec WHERE ec.provider_slug = 'shopify');
ALTER TABLE credential_provider DROP COLUMN IF EXISTS url_variables;
