-- Seed the trigger_type row for the Asana webhook trigger. Without it,
-- createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL and the trigger
-- silently fails to register. Each new integration seeds its own row.
INSERT INTO trigger_type (name) VALUES ('asana-webhook') ON CONFLICT DO NOTHING;
