-- Add parent-child execution linking (supports N-level depth)
ALTER TABLE execution ADD COLUMN IF NOT EXISTS parent_execution_id UUID REFERENCES execution(id);
ALTER TABLE execution ADD COLUMN IF NOT EXISTS agent_id UUID;
ALTER TABLE execution ADD COLUMN IF NOT EXISTS agent_session_id UUID;

CREATE INDEX IF NOT EXISTS idx_execution_parent ON execution(parent_execution_id) WHERE parent_execution_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_execution_agent ON execution(agent_id) WHERE agent_id IS NOT NULL;
