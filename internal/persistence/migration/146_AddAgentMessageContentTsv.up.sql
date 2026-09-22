-- Phase 1 of agent conversation-history search: full-text search over messages.
--
-- A GENERATED tsvector column keeps the index maintenance-free — Postgres
-- populates it at INSERT/UPDATE with no trigger and no backfill. to_tsvector
-- with a constant config is IMMUTABLE, so it is valid in a generated column.
ALTER TABLE agent_message
    ADD COLUMN content_tsv tsvector
    GENERATED ALWAYS AS (to_tsvector('english', coalesce(content, ''))) STORED;

CREATE INDEX idx_agent_message_content_tsv
    ON agent_message USING gin (content_tsv);
