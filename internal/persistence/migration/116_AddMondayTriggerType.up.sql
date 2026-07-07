-- Seed the trigger_type row for the Monday.com webhook trigger. Without it the
-- type resolves to NULL and the trigger silently fails to register.
INSERT INTO trigger_type (name) VALUES ('monday-webhook') ON CONFLICT DO NOTHING;
