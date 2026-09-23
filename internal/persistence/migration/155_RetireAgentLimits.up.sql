-- The agent carried three settings that nothing ever enforced: no
-- concurrency gate read max_concurrent_executions, the hourly count
-- query had no callers, and no approval gate existed. They promised
-- limits the platform never applied, so they go rather than stay as
-- controls that do nothing.
ALTER TABLE agent
    DROP COLUMN max_concurrent_executions,
    DROP COLUMN requires_approval,
    DROP COLUMN max_executions_per_hour;

-- The approval workflow on agent_execution was half-built and never
-- reached: nothing inserts the row, nothing sets approved_by, and no
-- endpoint approves anything.
DROP INDEX IF EXISTS idx_agent_execution_status;

ALTER TABLE agent_execution
    DROP COLUMN requires_approval,
    DROP COLUMN approved_by,
    DROP COLUMN approved_at;

CREATE INDEX idx_agent_execution_status ON agent_execution(agent_id, status) WHERE status = 'pending';

-- Which AI provider runs the memory extraction prompt. The extraction
-- flow is one shared system flow, so this is passed as trigger data and
-- switched on inside the flow rather than baked into the node.
ALTER TABLE agent ADD COLUMN extraction_provider TEXT NOT NULL DEFAULT 'anthropic';
