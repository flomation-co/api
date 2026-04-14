-- Migration 52: Memory hygiene support for Phase 7.
-- Adds status tracking, supersession chains, and pin governance.

-- a) status + superseded_by on agent_memory
ALTER TABLE agent_memory ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE agent_memory ADD COLUMN IF NOT EXISTS superseded_by UUID REFERENCES agent_memory(id) ON DELETE SET NULL;

-- b) max_pinned_memories config on agent (per-agent, default 50)
ALTER TABLE agent ADD COLUMN IF NOT EXISTS max_pinned_memories INTEGER DEFAULT 50;

-- c) Index for finding contradiction/dedup candidates efficiently
CREATE INDEX IF NOT EXISTS idx_agent_memory_hygiene_candidates
    ON agent_memory(agent_user_id, memory_type)
    WHERE status = 'active' AND embedding IS NOT NULL;

-- d) Partial index for counting pinned memories per user
CREATE INDEX IF NOT EXISTS idx_agent_memory_pinned_count
    ON agent_memory(agent_user_id)
    WHERE pinned = TRUE AND status = 'active';

-- e) Supersession chain lookups
CREATE INDEX IF NOT EXISTS idx_agent_memory_superseded_by
    ON agent_memory(superseded_by)
    WHERE superseded_by IS NOT NULL;
