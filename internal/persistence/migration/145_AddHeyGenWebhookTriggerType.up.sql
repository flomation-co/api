-- Seed the trigger_type row for the heygen-webhook trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL, which violates
-- the NOT NULL constraint on trigger.type, and the trigger fails to register --
-- SILENTLY from the operator's point of view: the flow saves 201, no trigger row
-- is written, and nothing is logged.
--
-- Pairs with launch migration 53, which adds 'heygen-webhook' to launch's
-- TriggerType enum. BOTH are required; the name must match the executor's trigger
-- id exactly. The api derives it by turning 'trigger/heygen_webhook' into
-- 'heygen-webhook' (underscores -> hyphens).
--
-- NUMBERING: 145. main is at 144 when this is written. golang-migrate SILENTLY
-- SKIPS an out-of-order version and a DUPLICATE version makes the api fail to
-- BOOT (502). Git does not flag a clash (filenames differ) and the Go tests do
-- not run migrations against a real database, so re-check this number AFTER
-- rebasing on main, not just now.
--
-- IDEMPOTENCY -- deliberately NOT "ON CONFLICT DO NOTHING": trigger_type.name has
-- no UNIQUE constraint (see migration 06), so ON CONFLICT has no arbiter index and
-- silently inserts a DUPLICATE row. A duplicate name then breaks the single-row
-- (SELECT id FROM trigger_type WHERE name = ...) subquery platform-wide with
-- "more than one row returned by a subquery". WHERE NOT EXISTS is genuinely
-- idempotent without relying on a constraint that is not there.
INSERT INTO trigger_type (name)
SELECT 'heygen-webhook'
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_type t WHERE t.name = 'heygen-webhook'
);
