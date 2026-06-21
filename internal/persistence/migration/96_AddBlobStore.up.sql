-- Promotes the executor-local disk blob store to a first-class
-- mTLS-gated storage tier owned by the API. Underpins file
-- attachment support across Telegram, Slack and any future channel
-- adapter — Launch and the executor both upload here, and the
-- executor reads here when resolving flo:blob:... tokens.
--
-- Design decisions baked in:
--   * Postgres bytea for v1 storage. 25 MB hard cap enforced in the
--     Go handler keeps row size sane. Swap to S3 in v2 is a contract-
--     preserving change (replace `content` with an S3 key column).
--   * org_id is the hard auth boundary: every read filters on it via
--     SQL, not middleware. Cross-org reads return 404, not 403, so
--     existence isn't leaked.
--   * execution_id is nullable because inbound attachments arrive at
--     Launch *before* an execution exists. The dispatch path stamps
--     the column retroactively once it has the execution ID.
--   * purpose drives TTL server-side. 'inbound' → 30 days,
--     'tool_output' → 1 hour. Callers cannot override either way.
--   * sha256 is indexed per-org for future dedup, but the v1
--     upload path does not short-circuit on duplicate content.

CREATE TABLE blob_object (
    handle           BYTEA       PRIMARY KEY,
    mime             TEXT        NOT NULL,
    size_bytes       BIGINT      NOT NULL,
    sha256           BYTEA       NOT NULL,
    content          BYTEA       NOT NULL,
    org_id           UUID        NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    execution_id     UUID                 REFERENCES execution(id)    ON DELETE SET NULL,
    purpose          TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT blob_object_purpose_valid
        CHECK (purpose IN ('inbound', 'tool_output', 'manual')),
    CONSTRAINT blob_object_size_positive
        CHECK (size_bytes >= 0),
    CONSTRAINT blob_object_size_cap
        CHECK (size_bytes <= 26214400)            -- 25 MB
);

-- Auth-scoped reads: every GET applies "WHERE handle = $1 AND org_id = $2".
CREATE INDEX blob_object_org_handle_idx
    ON blob_object (org_id, handle);

-- GC sweep: "WHERE expires_at < NOW() LIMIT N" runs on the poller.
CREATE INDEX blob_object_expires_idx
    ON blob_object (expires_at);

-- Cascade delete from execution: indexed for FK enforcement speed.
CREATE INDEX blob_object_execution_idx
    ON blob_object (execution_id)
    WHERE execution_id IS NOT NULL;

-- Per-org dedup lookup (deferred feature; index ready).
CREATE INDEX blob_object_org_sha256_idx
    ON blob_object (org_id, sha256);

-- Per-org daily upload quota tracking. Lives in a tiny counter table
-- so the upload handler can enforce a quota in a single UPSERT +
-- threshold check, rather than scanning blob_object.
CREATE TABLE blob_quota_daily (
    org_id     UUID        NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    quota_day  DATE        NOT NULL,
    bytes_used BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, quota_day)
);

-- GC sweep for the quota table: prune any day older than 7 days.
CREATE INDEX blob_quota_daily_day_idx
    ON blob_quota_daily (quota_day);
