-- Agent: autonomous entity that receives messages and dispatches flows
CREATE TABLE IF NOT EXISTS agent (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                        VARCHAR(255) NOT NULL,
    description                 TEXT,
    owner_id                    UUID NOT NULL REFERENCES users(id),
    organisation_id             UUID REFERENCES organisation(id),
    environment_id              UUID,
    queue_id                    UUID,
    system_prompt               TEXT,
    orchestrator_flow_id        UUID,
    max_concurrent_executions   INT NOT NULL DEFAULT 3,
    idle_timeout_seconds        INT NOT NULL DEFAULT 3600,
    channels                    JSONB NOT NULL DEFAULT '[]'::jsonb,
    allowed_flow_ids            UUID[],
    requires_approval           BOOLEAN NOT NULL DEFAULT FALSE,
    max_executions_per_hour     INT NOT NULL DEFAULT 100,
    status                      VARCHAR(20) NOT NULL DEFAULT 'stopped',
    started_at                  TIMESTAMPTZ,
    stopped_at                  TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at                 TIMESTAMPTZ
);

CREATE INDEX idx_agent_owner ON agent(owner_id);
CREATE INDEX idx_agent_org ON agent(organisation_id);
CREATE INDEX idx_agent_status ON agent(status) WHERE archived_at IS NULL;

-- Agent session: a period of continuous operation
CREATE TABLE IF NOT EXISTS agent_session (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at        TIMESTAMPTZ,
    status          VARCHAR(20) NOT NULL DEFAULT 'active',
    heartbeat_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    summary         JSONB DEFAULT '{}'::jsonb,
    error_message   TEXT
);

CREATE INDEX idx_agent_session_agent ON agent_session(agent_id);
CREATE INDEX idx_agent_session_active ON agent_session(agent_id, status) WHERE status = 'active';

-- Agent state: persistent key-value store surviving restarts
CREATE TABLE IF NOT EXISTS agent_state (
    agent_id    UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    state_key   VARCHAR(512) NOT NULL,
    state_value JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, state_key)
);

-- Agent message: inbound/outbound message log
CREATE TABLE IF NOT EXISTS agent_message (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    session_id      UUID REFERENCES agent_session(id),
    direction       VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound', 'system')),
    channel_type    VARCHAR(30) NOT NULL,
    sender          VARCHAR(255),
    content         TEXT NOT NULL,
    metadata        JSONB DEFAULT '{}'::jsonb,
    execution_id    UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_message_agent ON agent_message(agent_id);
CREATE INDEX idx_agent_message_session ON agent_message(session_id);
CREATE INDEX idx_agent_message_created ON agent_message(agent_id, created_at DESC);

-- Agent execution: tracks which flows an agent has dispatched
CREATE TABLE IF NOT EXISTS agent_execution (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id            UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    session_id          UUID REFERENCES agent_session(id),
    message_id          UUID REFERENCES agent_message(id),
    execution_id        UUID NOT NULL,
    flow_id             UUID NOT NULL,
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
    requires_approval   BOOLEAN NOT NULL DEFAULT FALSE,
    approved_by         UUID REFERENCES users(id),
    approved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_agent_execution_agent ON agent_execution(agent_id);
CREATE INDEX idx_agent_execution_session ON agent_execution(session_id);
CREATE INDEX idx_agent_execution_status ON agent_execution(agent_id, status) WHERE status IN ('pending', 'pending_approval');
