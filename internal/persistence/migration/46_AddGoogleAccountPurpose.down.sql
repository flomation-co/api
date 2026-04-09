ALTER TABLE agent_user_google_account
    DROP CONSTRAINT IF EXISTS agent_user_google_account_unique;

ALTER TABLE agent_user_google_account
    ADD CONSTRAINT agent_user_google_account_agent_user_id_google_email_key
    UNIQUE(agent_user_id, google_email);

ALTER TABLE agent_user_google_account
    DROP COLUMN IF EXISTS purpose;
