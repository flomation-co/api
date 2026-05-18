ALTER TABLE users ADD COLUMN IF NOT EXISTS last_activity_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_users_last_activity_at ON users(last_activity_at)
    WHERE last_activity_at IS NOT NULL;
