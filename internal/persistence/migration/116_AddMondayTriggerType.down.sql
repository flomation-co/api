-- Remove the monday-webhook trigger_type seed row. The launch-side down (36) is
-- a no-op (Postgres can't drop an enum value), so a full rollback leaves the
-- launch enum value orphaned but harmless.
DELETE FROM trigger_type WHERE name = 'monday-webhook';
