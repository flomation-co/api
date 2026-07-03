-- Seed the webhook trigger types that were missing from trigger_type.
--
-- createFloRevision resolves a flow's trigger type to a trigger_type UUID via
-- (SELECT id FROM trigger_type WHERE name = :type_name). When the row is
-- absent the subquery returns NULL, CreateTriggerWithType inserts a NULL type
-- and the insert fails silently, so the webhook trigger never registers with
-- Launch. The Zendesk, Calendly, Shopify and Mailchimp webhook triggers all
-- shipped without their trigger_type rows, so none of them register.
INSERT INTO trigger_type (name) VALUES
	('zendesk-webhook'),
	('calendly-webhook'),
	('shopify-webhook'),
	('mailchimp-webhook')
ON CONFLICT DO NOTHING;
