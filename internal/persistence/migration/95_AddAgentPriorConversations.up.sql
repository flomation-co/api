-- Surfaces past conversations to the agent's reasoning loop.
--
-- agent_memory.source_conversation ALREADY exists from migration 42 as
-- a nullable UUID FK; we don't need to add it. What's missing is a
-- targeted index for the "last N session_summary memories for this
-- agent+user" query that powers the new prior_conversations payload
-- on every inbound agent message — the existing user_type index is
-- close but not ordered by recency.
--
-- agent.prior_conversation_count is the per-agent knob that decides
-- how many summaries we ship in trigger data. Default 5 matches the
-- scoped recommendation; range is 0..50 enforced in the editor.

CREATE INDEX idx_agent_memory_session_summary_recent
    ON agent_memory(agent_id, agent_user_id, created_at DESC)
    WHERE memory_type = 'session_summary';

ALTER TABLE agent
    ADD COLUMN prior_conversation_count INTEGER NOT NULL DEFAULT 5;
