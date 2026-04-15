-- Widen direction column from VARCHAR(10) to VARCHAR(20) to accommodate
-- tool_result (11 chars). The CHECK constraint was already updated in
-- migration 53 to allow tool_use/tool_result values.
ALTER TABLE agent_message ALTER COLUMN direction TYPE VARCHAR(20);
