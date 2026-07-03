-- Seed the trigger_type row for the Cal.com webhook trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL and the trigger
-- silently fails to register. New integrations seed their own row in their own
-- migration (see 106_AddMissingWebhookTriggerTypes for why the repair had to be
-- a forward migration for the earlier integrations).
INSERT INTO trigger_type (name) VALUES ('calcom-webhook') ON CONFLICT DO NOTHING;
