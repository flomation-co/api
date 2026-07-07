-- Remove the asana-webhook trigger_type seed row. Note the asymmetry: this seed
-- deletes cleanly, but launch's matching down migration (35) is a no-op because
-- Postgres can't drop an enum value. A full rollback leaves the launch enum value
-- in place while the api seed is gone — expected and harmless.
DELETE FROM trigger_type WHERE name = 'asana-webhook';
