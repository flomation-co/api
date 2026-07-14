-- Seed the trigger_type row for the AWX webhook trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL, which violates
-- the NOT NULL constraint on trigger.type, and the trigger fails to register --
-- SILENTLY from the operator's point of view: the flow saves 201, no trigger row
-- is written, no notification template is created in AWX, and nothing is logged.
-- Each new integration seeds its own row.
--
-- Pairs with launch migration 45, which adds 'awx-webhook' to launch's TriggerType
-- enum. BOTH are required; the name must match the executor's trigger ID exactly
-- ('awx-webhook').
--
-- NUMBERING: 126, not 125. main was at 124_AddWebTriggerType when this was
-- written, but the typeform/jotform/surveymonkey trigger_type repair takes 125 and
-- merges first. golang-migrate SILENTLY SKIPS an out-of-order version, and a
-- DUPLICATE version makes the api fail to BOOT (502) -- exactly what happened when
-- 121_AddMqttTriggerType collided with 120_AddEmbedApps. Git does not flag it (the
-- filenames differ) and the Go tests do not run migrations against a real
-- database, so re-check this number AFTER rebasing on main, not just now.
-- IDEMPOTENCY -- deliberately NOT "ON CONFLICT DO NOTHING": trigger_type.name has
-- no UNIQUE constraint (see migration 06), so there is no arbiter index for
-- ON CONFLICT to match and it silently degrades to a guard that inserts a
-- DUPLICATE row anyway. A duplicate name then makes the single-row
-- (SELECT id FROM trigger_type WHERE name = ...) subquery above fail with
-- "more than one row returned by a subquery used as an expression", breaking
-- trigger registration platform-wide -- worse than the bug this seeds against.
-- WHERE NOT EXISTS is genuinely idempotent without relying on a constraint that
-- is not there.
INSERT INTO trigger_type (name)
SELECT 'awx-webhook'
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_type t WHERE t.name = 'awx-webhook'
);
