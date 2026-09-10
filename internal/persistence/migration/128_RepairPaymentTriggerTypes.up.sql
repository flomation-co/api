-- Repair migration: seed the trigger_type rows for the Stripe, QuickBooks and
-- Xero webhook triggers. These three integrations shipped with launch-side
-- constants (launch/types.go), full inbound handlers and OAuth providers, but no
-- migration ever seeded them on either side -- verified absent from trigger_type
-- on the dev stack and from the whole migration source on main.
--
-- Without the row, createFloRevision resolves the type via
--   (SELECT id FROM trigger_type WHERE name = :type_name)
-- -> NULL, which violates the NOT NULL constraint on trigger.type, so the trigger
-- fails to register: the flow saves 201, but no webhook is ever registered with
-- the provider and inbound submissions 404 -- silently. Same class of defect as
-- migration 126 (RepairFormTriggerTypes) and 106 (AddMissingWebhookTriggerTypes).
--
-- Pairs with launch migration 46, which adds the matching TriggerType enum
-- values. Both are required.
--
-- IDEMPOTENCY -- deliberately NOT "ON CONFLICT DO NOTHING": trigger_type.name has
-- no UNIQUE constraint (migration 06), so ON CONFLICT has no arbiter index and
-- degrades to a guard that still inserts a DUPLICATE row -- and a duplicate name
-- makes the single-row (SELECT id FROM trigger_type WHERE name = ...) lookup fail
-- with "more than one row returned by a subquery", breaking registration
-- platform-wide. WHERE NOT EXISTS is genuinely idempotent.
INSERT INTO trigger_type (name)
SELECT v.name
FROM (VALUES
    ('stripe-webhook'),
    ('quickbooks-webhook'),
    ('xero-webhook')
) AS v(name)
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_type t WHERE t.name = v.name
);
