DROP INDEX IF EXISTS idx_agent_memory_superseded_by;
DROP INDEX IF EXISTS idx_agent_memory_pinned_count;
DROP INDEX IF EXISTS idx_agent_memory_hygiene_candidates;
ALTER TABLE agent DROP COLUMN IF EXISTS max_pinned_memories;
ALTER TABLE agent_memory DROP COLUMN IF EXISTS superseded_by;
ALTER TABLE agent_memory DROP COLUMN IF EXISTS status;
