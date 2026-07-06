-- Seed the trigger_type row for the Jira webhook trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL and the trigger
-- silently fails to register. New integrations seed their own row in their own
-- migration (see 106_AddMissingWebhookTriggerTypes for the history).
INSERT INTO trigger_type (name) VALUES ('jira-webhook') ON CONFLICT DO NOTHING;
