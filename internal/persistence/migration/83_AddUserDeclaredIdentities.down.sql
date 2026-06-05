DROP INDEX IF EXISTS user_identity_lookup_idx;
DROP TABLE IF EXISTS user_identity;
DROP INDEX IF EXISTS users_anonymous_channel_idx;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_anon_has_channel_identity;
ALTER TABLE users
    DROP COLUMN IF EXISTS channel_external_id,
    DROP COLUMN IF EXISTS channel_type,
    DROP COLUMN IF EXISTS organisation_id,
    DROP COLUMN IF EXISTS is_anonymous;
