ALTER TABLE agent_memory DROP COLUMN IF EXISTS valid_until;
ALTER TABLE agent DROP COLUMN IF EXISTS memory_retention_days;
