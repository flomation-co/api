ALTER TABLE agent_user_google_account
    DROP COLUMN IF EXISTS access_token,
    DROP COLUMN IF EXISTS token_expires_at,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS last_error;

ALTER TABLE trigger_google_account
    DROP COLUMN IF EXISTS access_token,
    DROP COLUMN IF EXISTS token_expires_at,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS last_error;
