-- Revert Xero to the pre-granular broad scopes (as seeded by migration 122).
-- NOTE: broad scopes only work for Xero apps created before 2 March 2026; a
-- post-that-date managed app will get `invalid_scope` with these.
UPDATE credential_provider
SET default_scopes = 'openid profile email accounting.transactions accounting.contacts accounting.settings offline_access'
WHERE slug = 'xero';
