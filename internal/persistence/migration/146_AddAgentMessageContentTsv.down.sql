DROP INDEX IF EXISTS idx_agent_message_content_tsv;
ALTER TABLE agent_message DROP COLUMN IF EXISTS content_tsv;
