-- Reverse of 41_AddAgentMemoryPhase1.up.sql.
--
-- Drops are ordered so that FK constraints are released before the tables
-- they reference. Indexes are dropped implicitly with their tables.

-- 5. flo.system_flow
DROP INDEX IF EXISTS idx_flo_system_flow;
ALTER TABLE flo DROP COLUMN IF EXISTS system_flow_purpose;
ALTER TABLE flo DROP COLUMN IF EXISTS system_flow;

-- 4. agent_message updates
DROP INDEX IF EXISTS idx_agent_message_conversation_sequence;
DROP INDEX IF EXISTS idx_agent_message_conversation;
ALTER TABLE agent_message DROP COLUMN IF EXISTS sequence;
ALTER TABLE agent_message DROP COLUMN IF EXISTS conversation_id;

-- 3. agent_conversation
DROP TABLE IF EXISTS agent_conversation;

-- 2. agent_identity
DROP TABLE IF EXISTS agent_identity;

-- 1. agent_user
DROP TABLE IF EXISTS agent_user;