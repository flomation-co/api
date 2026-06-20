ALTER TABLE environment_credential       DROP COLUMN IF EXISTS consecutive_failures;
ALTER TABLE trigger_google_account       DROP COLUMN IF EXISTS consecutive_failures;
ALTER TABLE agent_user_google_account    DROP COLUMN IF EXISTS consecutive_failures;
