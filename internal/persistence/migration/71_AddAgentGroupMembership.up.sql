-- Agent group membership — allows agents to be assigned to organisation
-- groups and receive RBAC permissions, just like human users.
-- Uses a separate table to avoid modifying the existing PK/FK constraints
-- on organisation_group_member.

CREATE TABLE IF NOT EXISTS organisation_group_agent (
    group_id    UUID NOT NULL REFERENCES organisation_group(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    added_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_org_group_agent_agent ON organisation_group_agent(agent_id);
