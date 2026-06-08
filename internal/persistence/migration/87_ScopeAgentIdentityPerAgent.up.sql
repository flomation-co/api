-- CRITICAL FIX: per-agent scope on agent_identity to prevent
-- cross-agent memory leakage for the same human.
--
-- The original unique index in migration 41 keyed agent_identity rows on
-- (channel_type, channel_external_id, scope) globally — no agent_id in
-- the key. This caused:
--   * The first agent to converse with a given external identity created
--     an agent_identity → agent_user row.
--   * A second agent later receiving a message from the same external
--     identity (same channel, same external_id) was returned the SAME
--     existing identity by ResolveOrCreateAgentIdentity and inherited
--     the first agent's agent_user_id.
--   * agent_memory rows scoped to that agent_user_id were therefore
--     visible to every subsequent agent the human spoke to.
--
-- The leak is bounded to bots-within-the-same-human (different humans
-- have different external_ids and so distinct identity rows). No data
-- has crossed between different end-users.
--
-- Fix: denormalise agent_id onto agent_identity so each agent has its
-- own identity row per (channel_type, external_id, scope). Backfill
-- from the owning agent_user row, then re-create the unique index
-- including agent_id.

ALTER TABLE agent_identity ADD COLUMN agent_id UUID;

-- Backfill from the agent_user that already owns each identity.
UPDATE agent_identity ai
   SET agent_id = au.agent_id
  FROM agent_user au
 WHERE au.id = ai.agent_user_id;

ALTER TABLE agent_identity ALTER COLUMN agent_id SET NOT NULL;
ALTER TABLE agent_identity ADD CONSTRAINT agent_identity_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agent(id) ON DELETE CASCADE;

-- Drop the leaky global-scope index.
DROP INDEX IF EXISTS idx_agent_identity_channel;

-- Create per-agent unique index. Two agents may now hold an
-- agent_identity row for the same (channel_type, external_id, scope)
-- because they're distinct entries from the same human's perspective.
CREATE UNIQUE INDEX idx_agent_identity_agent_channel
    ON agent_identity(agent_id, channel_type, channel_external_id, COALESCE(channel_scope, ''));

CREATE INDEX IF NOT EXISTS idx_agent_identity_agent ON agent_identity(agent_id);
