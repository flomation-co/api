-- Reverting reintroduces the cross-agent leak. Provided only for
-- migration symmetry; in practice this should never be applied to a
-- production database that has acquired per-agent rows because the
-- old unique constraint would collide on the first duplicate.

DROP INDEX IF EXISTS idx_agent_identity_agent;
DROP INDEX IF EXISTS idx_agent_identity_agent_channel;

ALTER TABLE agent_identity DROP CONSTRAINT IF EXISTS agent_identity_agent_id_fkey;
ALTER TABLE agent_identity DROP COLUMN IF EXISTS agent_id;

CREATE UNIQUE INDEX idx_agent_identity_channel
    ON agent_identity(channel_type, channel_external_id, COALESCE(channel_scope, ''));
