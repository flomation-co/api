DROP INDEX IF EXISTS idx_users_marketing_sync_error;
ALTER TABLE users
    DROP COLUMN IF EXISTS welcome_completed_at,
    DROP COLUMN IF EXISTS marketing_synced_at,
    DROP COLUMN IF EXISTS marketing_sync_error;
