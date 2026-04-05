-- Phase 2 of the Agent Memory feature.
--
-- Adds the three tables that carry the real memory payload on top of
-- the Phase 1 identity + conversation scaffolding:
--
--   1. agent_memory         — durable facts, preferences, feedback, and
--                             session summaries attached to an agent_user.
--   2. agent_pending_action — natural-language intents that need user
--                             confirmation before executing (identity
--                             linking, memory forgetting, corrections).
--   3. agent_commitment     — promises the agent (or user) has made that
--                             need to be honoured on a schedule or when
--                             a condition is met. Phase 3 adds the
--                             poller that fires these.
--
-- Deliberately NO `embedding VECTOR(1536)` column on agent_memory yet —
-- semantic retrieval with pgvector lands in Phase 4. Phase 2 retrieval
-- is pinned-memory-always-included plus type-filtered lookups, which is
-- sufficient for the feature's user-visible benefits (preferences that
-- survive across sessions) without pulling the pgvector extension into
-- on-prem installations before it's needed.
--
-- See plans/agent_memory.md §"Memory records", §"Pending actions",
-- §"Commitments" for the full design rationale.

-- 1. agent_memory: the durable facts an agent has learnt about a user.
--
-- scope='user' means the memory is attached to a specific agent_user
-- (the vast majority of rows). scope='global' means the memory applies
-- to the agent itself regardless of who it's talking to — used sparingly
-- for agent-wide facts like "this agent operates in UTC".
--
-- memory_type is the retrieval key that decides how the memory is
-- surfaced into the assembled system prompt. 'preference' and 'feedback'
-- are auto-pinned for always-include behaviour; 'fact', 'relationship',
-- and 'session_summary' are fetched by semantic/type lookup in Phase 4.
-- 'disputed_claim' is never retrieved for prompting and exists only for
-- audit (e.g. "user claimed to be @sarah but did not verify").
--
-- confidence is written by the extraction pipeline (0.0–1.0). Memories
-- with confidence >= 0.8 are auto-stored; 0.5–0.8 are stored but flagged
-- for user review; < 0.5 are discarded by the extraction flow before
-- ever reaching this table. The threshold check lives in the extraction
-- flow, not in the schema, so admins can retune it without a migration.
--
-- pinned is set by the extraction pipeline or by an explicit flow-author
-- call to `agent/remember` with pinned=true. Pinned memories are always
-- included in the assembled system prompt regardless of retrieval rules.
CREATE TABLE IF NOT EXISTS agent_memory (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id            UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id       UUID REFERENCES agent_user(id) ON DELETE CASCADE,
    scope               TEXT NOT NULL,        -- 'user' | 'global'
    memory_type         TEXT NOT NULL,        -- 'preference' | 'feedback' | 'fact' | 'relationship' | 'task' | 'session_summary' | 'disputed_claim'
    title               TEXT NOT NULL,
    body                TEXT NOT NULL,
    source_conversation UUID REFERENCES agent_conversation(id) ON DELETE SET NULL,
    source_message      UUID REFERENCES agent_message(id) ON DELETE SET NULL,
    confidence          REAL NOT NULL DEFAULT 1.0,
    pinned              BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ
);

-- Pinned memories for a user: the hot path the system-prompt assembler
-- hits on every turn. Partial index keeps it small (only pinned rows).
CREATE INDEX IF NOT EXISTS idx_agent_memory_user_pinned
    ON agent_memory(agent_user_id, pinned)
    WHERE agent_user_id IS NOT NULL;

-- Type-based retrieval for Phase 2 (before pgvector arrives in Phase 4).
-- Extraction writes multiple memory_types in a single turn; the assembler
-- reads them back per-type to build grouped sections of the system prompt.
CREATE INDEX IF NOT EXISTS idx_agent_memory_user_type
    ON agent_memory(agent_user_id, memory_type)
    WHERE agent_user_id IS NOT NULL;

-- Agent-wide lookups (admin UI, retention jobs, audit exports).
CREATE INDEX IF NOT EXISTS idx_agent_memory_agent
    ON agent_memory(agent_id);

-- Expiry index used by the retention System Flow in Phase 6 to cheaply
-- find memories past their TTL. Partial index avoids touching rows that
-- never expire (expires_at IS NULL).
CREATE INDEX IF NOT EXISTS idx_agent_memory_expires
    ON agent_memory(expires_at)
    WHERE expires_at IS NOT NULL;

-- 2. agent_pending_action: intents inferred from natural language that
-- need user confirmation before executing. This is the backbone of every
-- natural-language operation that can't be auto-committed — identity
-- linking, memory forgetting, memory correction, profile wipes, etc.
--
-- type is a free-text classifier written by the extraction pipeline:
--   - 'identity_link'  (Phase 5) — user claims to also be X on another channel
--   - 'forget_memory'  (Phase 2d) — user asks to forget a specific fact
--   - 'correct_memory' (Phase 2d) — user asks to replace an existing memory
--   - future types added by the extraction flow without schema changes.
--
-- payload is the structured version of the intent (target memory IDs,
-- proposed identity tuple, etc.). evidence is the verbatim user utterance
-- that triggered the detection, retained for audit and for the agent's
-- confirmation prompt ("you said X — is that right?").
--
-- status transitions:
--   awaiting_confirmation → confirmed_here_awaiting_other_side (Phase 5)
--                         → executed
--                         → declined
--                         → expired  (past expires_at, typically 24h)
CREATE TABLE IF NOT EXISTS agent_pending_action (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id            UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id       UUID NOT NULL REFERENCES agent_user(id) ON DELETE CASCADE,
    type                TEXT NOT NULL,
    payload             JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence            TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'awaiting_confirmation',
    source_conversation UUID REFERENCES agent_conversation(id) ON DELETE SET NULL,
    source_message      UUID REFERENCES agent_message(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at         TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ
);

-- Launch hits this on every turn to check for pending confirmations for
-- the current agent_user. Partial index restricts to the open states so
-- resolved/declined/expired rows don't bloat it.
CREATE INDEX IF NOT EXISTS idx_agent_pending_action_user_open
    ON agent_pending_action(agent_user_id, created_at DESC)
    WHERE status IN ('awaiting_confirmation', 'confirmed_here_awaiting_other_side');

-- Expiry sweeps for the retention job.
CREATE INDEX IF NOT EXISTS idx_agent_pending_action_expires
    ON agent_pending_action(expires_at)
    WHERE expires_at IS NOT NULL AND status IN ('awaiting_confirmation', 'confirmed_here_awaiting_other_side');

-- 3. agent_commitment: promises that need to be honoured on a schedule or
-- condition. Detected by the same extraction pipeline as memories and
-- pending actions, but with a different lifecycle — a commitment is
-- waiting for *time or a signal*, not for user confirmation.
--
-- kind:
--   'followup'  — "I'll come back to you in an hour" (time_elapsed)
--   'reminder'  — "Remind me tomorrow at 9" (absolute_time)
--   'monitor'   — "Let me know when the build finishes" (condition, Phase 3+)
--   'chase'     — "Check if Sarah replied, in 15 minutes" (recurring)
--
-- trigger_type selects the poller's wake-up strategy:
--   'time_elapsed' | 'absolute_time' | 'condition' | 'user_prompt'
--
-- status transitions:
--   pending → firing   (commitment poller claimed it, wake-up in flight)
--           → fulfilled (wake-up flow completed, response delivered)
--           → cancelled (user or admin cancelled before firing)
--           → expired   (past expires_at without firing)
--
-- Phase 2a creates this table so the extraction pipeline in Phase 2d can
-- write to it, and so the Phase 3 commitment poller has a stable schema
-- to select from. The poller itself is NOT built in Phase 2 — rows will
-- accumulate in 'pending' status until Phase 3 ships.
CREATE TABLE IF NOT EXISTS agent_commitment (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id            UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    agent_user_id       UUID REFERENCES agent_user(id) ON DELETE CASCADE,
    conversation_id     UUID REFERENCES agent_conversation(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL,
    description         TEXT NOT NULL,
    payload             JSONB NOT NULL DEFAULT '{}'::jsonb,
    trigger_type        TEXT NOT NULL,
    due_at              TIMESTAMPTZ,
    condition           JSONB,
    status              TEXT NOT NULL DEFAULT 'pending',
    source_conversation UUID REFERENCES agent_conversation(id) ON DELETE SET NULL,
    source_message      UUID REFERENCES agent_message(id) ON DELETE SET NULL,
    made_by             TEXT NOT NULL DEFAULT 'assistant',   -- 'assistant' | 'user'
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    fired_at            TIMESTAMPTZ,
    fulfilled_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ
);

-- The Phase 3 commitment poller's hot-path query:
--   SELECT ... WHERE status='pending' AND due_at < NOW()
-- Partial index restricted to pending rows, ordered by due_at ASC so the
-- poller can process oldest-first.
CREATE INDEX IF NOT EXISTS idx_agent_commitment_due_pending
    ON agent_commitment(due_at)
    WHERE status = 'pending';

-- Per-user commitment listing for the profile page in Phase 6 and for the
-- extraction pipeline's "find recent commitments for this user" lookup
-- when detecting fulfilments in assistant replies.
CREATE INDEX IF NOT EXISTS idx_agent_commitment_user
    ON agent_commitment(agent_user_id, created_at DESC)
    WHERE agent_user_id IS NOT NULL;

-- Conversation-scoped lookup used when a commitment fires and Launch
-- needs to reconstruct the target channel to deliver the wake-up reply
-- back where the promise was made.
CREATE INDEX IF NOT EXISTS idx_agent_commitment_conversation
    ON agent_commitment(conversation_id)
    WHERE conversation_id IS NOT NULL;