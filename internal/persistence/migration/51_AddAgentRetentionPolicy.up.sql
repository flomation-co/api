-- Migration 51: Retention policy support for Phase 6.
-- memory_retention_days on agent controls automatic expiry.
-- valid_until on agent_memory allows per-memory TTL.

ALTER TABLE agent ADD COLUMN IF NOT EXISTS memory_retention_days INTEGER;

ALTER TABLE agent_memory ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ;
