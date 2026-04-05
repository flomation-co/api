-- Phase 1 of the Agent Memory feature.
--
-- Introduces the core identity + session concepts that subsequent phases
-- build on. See plans/agent_memory.md for the full design document.
--
-- Phase 1 scope:
--   1. agent_user        — the canonical person an agent knows about.
--   2. agent_identity    — a per-channel identity mapping to an agent_user.
--   3. agent_conversation — a conversation thread scoped to a specific
--                          channel + thread + agent_user. Distinct from
--                          the existing agent_session table, which tracks
--                          runtime lifecycle (active/ended/crashed) rather
--                          than conversation scoping.
--   4. agent_message updates — new conversation_id and sequence columns
--                              that link messages to their conversation
--                              and enforce ordering within it.
--   5. flo.system_flow flag  — marks platform-managed flows so they can be
--                              hidden from the default Flows and Executions
--                              list views.

-- 1. agent_user: the canonical person, independent of which channel they
-- reach the agent on. Memories, commitments, and linked identities all
-- hang off this record.
CREATE TABLE IF NOT EXISTS agent_user (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    organisation_id UUID REFERENCES organisation(id),
    display_name    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_user_agent ON agent_user(agent_id);
CREATE INDEX idx_agent_user_org   ON agent_user(organisation_id);

-- 2. agent_identity: a per-channel identity that maps to an agent_user.
-- A single agent_user may have multiple identities (e.g. Slack + Telegram)
-- linked together via the natural-language identity linking flow landing
-- in Phase 5. Until then, each identity corresponds to exactly one
-- agent_user and memories are scoped per-identity.
--
-- `verified` starts false for any auto-created identity. It becomes true
-- only after a two-sided natural-language confirmation in Phase 5.
CREATE TABLE IF NOT EXISTS agent_identity (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_user_id       UUID NOT NULL REFERENCES agent_user(id) ON DELETE CASCADE,
    channel_type        TEXT NOT NULL,   -- 'slack' | 'telegram' | 'webhook' | 'email' | 'form' | 'sso'
    channel_external_id TEXT NOT NULL,   -- Slack user_id, Telegram sender_id, email addr, etc.
    channel_scope       TEXT,            -- Slack team_id, Telegram bot_id, SSO issuer — disambiguator
    verified            BOOLEAN NOT NULL DEFAULT FALSE,
    linked_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A given (channel_type, channel_external_id, channel_scope) tuple must
-- resolve to exactly one identity across the whole database. NULL scope
-- is treated as its own bucket (Postgres default semantics for UNIQUE).
CREATE UNIQUE INDEX idx_agent_identity_channel
    ON agent_identity(channel_type, channel_external_id, COALESCE(channel_scope, ''));
CREATE INDEX idx_agent_identity_user ON agent_identity(agent_user_id);

-- 3. agent_conversation: a conversation thread in a specific channel.
-- Sessions in the plan document map to this table. The name is
-- deliberately `agent_conversation` rather than `agent_session` because
-- the existing `agent_session` table (from migration 35) tracks runtime
-- lifecycle (active/ended/crashed + heartbeat) rather than conversation
-- scoping. The two concerns coexist and both are needed.
--
-- Uniqueness is keyed on (agent, channel_type, channel_id, thread_id).
-- thread_id is NULL for channels without native threading; in that case
-- a fresh conversation is started on demand or when an explicit "new
-- conversation" signal arrives (Phase 4+).
CREATE TABLE IF NOT EXISTS agent_conversation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id   UUID REFERENCES agent_user(id) ON DELETE SET NULL,
    channel_type    TEXT NOT NULL,
    channel_id      TEXT NOT NULL,       -- Slack channel, Telegram chat, email thread root
    thread_id       TEXT,                -- Slack thread_ts, reply-chain root; NULL = top-level
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at        TIMESTAMPTZ,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE UNIQUE INDEX idx_agent_conversation_key
    ON agent_conversation(agent_id, channel_type, channel_id, COALESCE(thread_id, ''))
    WHERE ended_at IS NULL;
CREATE INDEX idx_agent_conversation_user ON agent_conversation(agent_user_id);
CREATE INDEX idx_agent_conversation_agent ON agent_conversation(agent_id);
CREATE INDEX idx_agent_conversation_last_message
    ON agent_conversation(agent_id, last_message_at DESC);

-- 4. agent_message updates. The existing table (from migration 35) is
-- used for lifecycle message logging; extending it in place preserves
-- backward compatibility with existing agent executions. Two new columns:
--
--   - conversation_id: links each message to the conversation it belongs
--     to. Nullable for backwards compatibility with pre-existing rows
--     that predate conversations. New rows from Phase 1 onwards will
--     always have it populated.
--
--   - sequence: monotonic ordering within a conversation. Phase 1 sets
--     this at insert time by taking MAX(sequence)+1 for the target
--     conversation. Not globally unique; unique within a conversation.
ALTER TABLE agent_message
    ADD COLUMN IF NOT EXISTS conversation_id UUID REFERENCES agent_conversation(id) ON DELETE CASCADE;

ALTER TABLE agent_message
    ADD COLUMN IF NOT EXISTS sequence BIGINT;

CREATE INDEX IF NOT EXISTS idx_agent_message_conversation
    ON agent_message(conversation_id, sequence)
    WHERE conversation_id IS NOT NULL;

-- Uniqueness of sequence within a conversation, but only for messages
-- that have been assigned to a conversation. Existing unassigned rows
-- (conversation_id IS NULL) are excluded by the partial index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_message_conversation_sequence
    ON agent_message(conversation_id, sequence)
    WHERE conversation_id IS NOT NULL AND sequence IS NOT NULL;

-- 5. flo.system_flow: marks a flow as platform-managed so it can be
-- hidden from the default Flows and Executions list views. Extraction
-- flows, commitment-poller flows, session-summarisation flows, and any
-- other System Flow from subsequent phases set this to TRUE.
--
-- Flows with system_flow=TRUE are:
--   - Filtered out of GET /api/v1/flo by default (admins can opt in
--     via ?include_system=true).
--   - Filtered out of GET /api/v1/execution by default (same override).
--   - Not editable by non-admin users.
--   - Still executable on the normal execution engine, so there's no
--     forked codepath to maintain.
--
-- system_flow_purpose is a short classifier used for diagnostics and
-- for admin UI grouping (e.g. "agent_extraction", "agent_commitment_poller").
ALTER TABLE flo
    ADD COLUMN IF NOT EXISTS system_flow BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE flo
    ADD COLUMN IF NOT EXISTS system_flow_purpose TEXT;

CREATE INDEX IF NOT EXISTS idx_flo_system_flow
    ON flo(system_flow) WHERE system_flow = TRUE;