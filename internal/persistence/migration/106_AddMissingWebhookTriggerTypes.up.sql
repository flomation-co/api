-- Repair migration: the already-shipped Shopify, Calendly and Mailchimp webhook
-- integrations never seeded their trigger_type rows, so their triggers never
-- register (createFloRevision resolves the type via
-- (SELECT id FROM trigger_type WHERE name = :type_name) -> NULL -> silent insert
-- failure). These integrations are already live on some environments, so their
-- original migrations can't be edited to fix them (migrations are immutable once
-- applied); a forward migration is required. New integrations should instead
-- seed their own trigger_type row in their own creation migration.
INSERT INTO trigger_type (name) VALUES
	('calendly-webhook'),
	('shopify-webhook'),
	('mailchimp-webhook')
ON CONFLICT DO NOTHING;
