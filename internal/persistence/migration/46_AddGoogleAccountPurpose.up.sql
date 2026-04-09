-- Add purpose-scoped tokens to Google account connections.
-- Each (agent_user, email, purpose) combination gets its own refresh
-- token with exactly the scopes needed for that purpose. This allows
-- users to grant calendar access without email, or read-only email
-- without send permission.

ALTER TABLE agent_user_google_account
    ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT 'calendar';

-- Drop the old unique constraint and create the new triple-scoped one.
-- Existing rows all get purpose='calendar' from the DEFAULT above.
ALTER TABLE agent_user_google_account
    DROP CONSTRAINT IF EXISTS agent_user_google_account_agent_user_id_google_email_key;

ALTER TABLE agent_user_google_account
    ADD CONSTRAINT agent_user_google_account_unique
    UNIQUE(agent_user_id, google_email, purpose);
