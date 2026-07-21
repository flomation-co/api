-- Seed the trigger_type row for the rds-event poll trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL, which violates
-- the NOT NULL constraint on trigger.type, and the trigger fails to register --
-- SILENTLY from the operator's point of view: the flow saves 201, no trigger row
-- is written, and nothing is logged.
--
-- Pairs with launch migration 48, which adds 'rds-event' to launch's TriggerType
-- enum. BOTH are required; the name must match the executor's trigger id exactly.
-- The api derives it by turning 'trigger/rds_event' into 'rds-event' (underscores
-- -> hyphens).
--
-- NUMBERING: 132. main is at 131 (131_AddDatabaseRowTriggerType) when this is
-- written. golang-migrate SILENTLY SKIPS an out-of-order version and a DUPLICATE
-- makes the api fail to BOOT (502). Git does not flag a clash (filenames differ)
-- and the Go tests do not run migrations against a real database, so re-check this
-- number AFTER rebasing on main, not just now.
--
-- IDEMPOTENCY -- deliberately NOT "ON CONFLICT DO NOTHING": trigger_type.name has
-- no UNIQUE constraint (see migration 06), so ON CONFLICT has no arbiter index and
-- silently inserts a DUPLICATE row, which then breaks the single-row
-- (SELECT id FROM trigger_type WHERE name = ...) subquery platform-wide. WHERE NOT
-- EXISTS is genuinely idempotent without relying on a constraint that isn't there.
INSERT INTO trigger_type (name)
SELECT 'rds-event'
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_type t WHERE t.name = 'rds-event'
);
