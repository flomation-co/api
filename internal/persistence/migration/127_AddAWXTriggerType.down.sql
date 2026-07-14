-- Remove the awx-webhook trigger_type seed row. Note the asymmetry with the
-- launch side: this seed row deletes cleanly, but launch's matching down migration
-- (45_AddAWXTriggerType) is a no-op because Postgres cannot drop an enum value. So
-- a full rollback across both services leaves the launch enum label in place while
-- the api seed row is gone -- expected, and harmless (the orphaned label is simply
-- unused).
DELETE FROM trigger_type WHERE name = 'awx-webhook';
