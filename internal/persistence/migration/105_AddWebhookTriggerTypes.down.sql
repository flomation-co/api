-- WARNING: Shopify/Calendly/Mailchimp are already-shipped integrations, so
-- these trigger types may be referenced by live trigger rows (trigger.type is
-- an FK to trigger_type.id). Rolling this back would orphan those triggers /
-- fail on the FK. Only run where no flows use these webhook triggers.
DELETE FROM trigger_type WHERE name IN ('zendesk-webhook', 'calendly-webhook', 'shopify-webhook', 'mailchimp-webhook');
