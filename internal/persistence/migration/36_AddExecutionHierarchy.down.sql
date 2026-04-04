DROP INDEX IF EXISTS idx_execution_agent;
DROP INDEX IF EXISTS idx_execution_parent;
ALTER TABLE execution DROP COLUMN IF EXISTS agent_session_id;
ALTER TABLE execution DROP COLUMN IF EXISTS agent_id;
ALTER TABLE execution DROP COLUMN IF EXISTS parent_execution_id;
