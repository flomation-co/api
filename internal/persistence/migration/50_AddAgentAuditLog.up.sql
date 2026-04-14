-- Migration 50: Agent audit log for Phase 6 memory management.
-- Tracks all write operations on agent data (memory, identity, pending
-- actions) for compliance, debugging, and the user-facing audit trail.

CREATE TABLE IF NOT EXISTS agent_audit_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id   UUID REFERENCES agent_user(id) ON DELETE SET NULL,
    actor_type      TEXT NOT NULL,
    actor_id        TEXT,
    event_type      TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     UUID,
    detail          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_audit_log_agent
    ON agent_audit_log(agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_agent_audit_log_user
    ON agent_audit_log(agent_user_id, created_at DESC)
    WHERE agent_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_agent_audit_log_resource
    ON agent_audit_log(resource_type, resource_id);