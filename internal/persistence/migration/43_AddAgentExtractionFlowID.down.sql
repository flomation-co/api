-- Reverse of 43_AddAgentExtractionFlowID.up.sql.

DROP INDEX IF EXISTS idx_agent_extraction_flow;
ALTER TABLE agent DROP COLUMN IF EXISTS extraction_flow_id;