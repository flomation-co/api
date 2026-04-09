-- AI API key for the extraction System Flow. Stored on the agent so
-- system flows don't depend on user-managed environments.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS ai_api_key BYTEA;
