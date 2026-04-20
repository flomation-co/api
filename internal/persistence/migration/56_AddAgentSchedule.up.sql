CREATE TABLE IF NOT EXISTS agent_schedule (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id   TEXT,
    conversation_id TEXT,
    name            VARCHAR(255) NOT NULL,
    description     TEXT NOT NULL,
    schedule_mode   VARCHAR(20) NOT NULL,
    interval_val    VARCHAR(10),
    unit            VARCHAR(10),
    time_of_day     VARCHAR(5),
    days_of_week    VARCHAR(100),
    timezone        VARCHAR(100) NOT NULL DEFAULT 'UTC',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    last_fired_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_schedule_agent   ON agent_schedule(agent_id);
CREATE INDEX idx_agent_schedule_enabled ON agent_schedule(agent_id) WHERE enabled = true;
