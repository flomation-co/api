ALTER TABLE users
    DROP COLUMN IF EXISTS marketing_consent_at,
    DROP COLUMN IF EXISTS marketing_consent_source,
    DROP COLUMN IF EXISTS marketing_consent_version;
