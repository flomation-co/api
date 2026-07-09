-- Seed the trigger_type row for the SendGrid webhook trigger. Without it the
-- type resolves to NULL and the trigger silently fails to register.
INSERT INTO trigger_type (name) VALUES ('sendgrid-webhook') ON CONFLICT DO NOTHING;
