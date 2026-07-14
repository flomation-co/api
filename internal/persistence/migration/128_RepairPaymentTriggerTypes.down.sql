-- Remove the three seeded rows (only if no trigger references them).
DELETE FROM trigger_type
WHERE name IN ('stripe-webhook', 'quickbooks-webhook', 'xero-webhook');
