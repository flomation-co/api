ALTER TABLE agent DROP COLUMN IF EXISTS prior_conversation_count;

DROP INDEX IF EXISTS idx_agent_memory_session_summary_recent;
