DROP INDEX IF EXISTS idx_users_last_activity_at;
ALTER TABLE users DROP COLUMN IF EXISTS last_activity_at;
