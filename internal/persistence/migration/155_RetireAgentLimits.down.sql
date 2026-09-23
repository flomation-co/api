ALTER TABLE agent DROP COLUMN extraction_provider;

DROP INDEX IF EXISTS idx_agent_execution_status;

ALTER TABLE agent_execution
    ADD COLUMN requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN approved_by UUID REFERENCES users(id),
    ADD COLUMN approved_at TIMESTAMPTZ;

CREATE INDEX idx_agent_execution_status ON agent_execution(agent_id, status) WHERE status IN ('pending', 'pending_approval');

ALTER TABLE agent
    ADD COLUMN max_concurrent_executions INT NOT NULL DEFAULT 3,
    ADD COLUMN requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN max_executions_per_hour INT NOT NULL DEFAULT 100;
