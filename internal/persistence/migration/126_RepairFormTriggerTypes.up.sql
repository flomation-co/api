-- Repair migration: seed the trigger_type rows for the Typeform, Jotform and
-- SurveyMonkey webhook triggers. These three integrations shipped with launch-side
-- constants (launch/types.go) and full inbound handlers, but no migration ever
-- seeded them on either side — verified absent from trigger_type in production.
--
-- Without the row, createFloRevision resolves the type via
--   (SELECT id FROM trigger_type WHERE name = :type_name)
-- -> NULL, which violates the NOT NULL constraint on trigger.type, so the trigger
-- fails to register: the flow saves 201, but no webhook is ever registered with
-- the provider and inbound submissions 404 — silently. Same class of defect as
-- migration 106 (AddMissingWebhookTriggerTypes) and launch's migration 31
-- (RepairCalcomAcuityTriggerType).
--
-- The names match the api's own derivation from the executor node label
-- (trigger/typeform_webhook -> "typeform-webhook": strip the "trigger/" prefix,
-- "_" -> "-"; see internal/http/flow.go) and launch's TriggerType enum.
--
-- Pairs with launch migration 44, which adds the matching TriggerType enum values.
-- Both are required.
--
-- IDEMPOTENCY — deliberately NOT "ON CONFLICT DO NOTHING": trigger_type.name has
-- no UNIQUE constraint (see migration 06), so there is no arbiter index for
-- ON CONFLICT to match and it degrades to a no-op guard that still inserts a
-- duplicate row. That matters here because production is broken right now and may
-- be hand-patched before this ships: a duplicate name would make the
-- single-row (SELECT id FROM trigger_type WHERE name = ...) subquery above fail
-- with "more than one row returned by a subquery used as an expression",
-- breaking registration worse than the bug being fixed. WHERE NOT EXISTS is
-- genuinely idempotent without relying on a constraint that isn't there.
INSERT INTO trigger_type (name)
SELECT v.name
FROM (VALUES
    ('typeform-webhook'),
    ('jotform-webhook'),
    ('surveymonkey-webhook')
) AS v(name)
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_type t WHERE t.name = v.name
);
