DROP INDEX IF EXISTS idx_agent_message_source_conversation;
ALTER TABLE agent_message DROP COLUMN IF EXISTS source_conversation_id;
