-- Human-in-the-Loop (Await Response) support.
--
-- hitl_request is the authoritative record of an outstanding human decision:
-- one row per Await node execution (enforced by the UNIQUE on
-- (execution_id, node_id) so a runner retry before the suspend persists is an
-- idempotent upsert). options holds [{value,label,token}]; channels holds the
-- delivered message references so losing channels can be updated once answered.
--
-- First-response-wins is enforced by a single conditional UPDATE:
--   UPDATE hitl_request SET status='answered', ... WHERE id=$1 AND status='awaiting'
-- Whichever of {a channel response, the timeout poller} wins that update drives
-- the one resume; the loser sees zero rows affected and no-ops.
CREATE TABLE hitl_request (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id     UUID NOT NULL REFERENCES execution(id) ON DELETE CASCADE,
    flo_id           UUID NOT NULL,
    node_id          TEXT NOT NULL,
    message          TEXT NOT NULL DEFAULT '',
    options          JSONB NOT NULL DEFAULT '[]'::jsonb,   -- [{value,label,token}]
    channels         JSONB NOT NULL DEFAULT '[]'::jsonb,   -- [{channel_type,node_id,channel_id,message_ref}]
    status           TEXT NOT NULL DEFAULT 'awaiting'
                     CHECK (status IN ('awaiting', 'answered', 'timed_out')),
    answered_option  TEXT,
    answered_by      TEXT,
    answered_channel TEXT,
    expires_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    answered_at      TIMESTAMPTZ,
    UNIQUE (execution_id, node_id)
);

-- Drives the timeout poller's scan for still-open, expired requests.
CREATE INDEX idx_hitl_request_status_expires ON hitl_request(status, expires_at);

-- Opaque per-option capability used by the channel-agnostic web click-link
-- fallback (and as Telegram callback_data, which is limited to 64 bytes). A
-- token resolves to exactly one (request, option).
CREATE TABLE hitl_token (
    token        TEXT PRIMARY KEY,
    request_id   UUID NOT NULL REFERENCES hitl_request(id) ON DELETE CASCADE,
    option_value TEXT NOT NULL
);
CREATE INDEX idx_hitl_token_request ON hitl_token(request_id);

-- resume_data carries the injected answer for audit/idempotency. The value is
-- also patched into the execution's checkpoint JSONB (top-level "resume_data")
-- at resume time so it reaches the executor untouched by the runner.
ALTER TABLE execution ADD COLUMN IF NOT EXISTS resume_data JSONB;
