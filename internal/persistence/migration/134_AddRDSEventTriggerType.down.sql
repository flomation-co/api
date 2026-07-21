-- Remove the rds-event trigger_type seed row. Asymmetric with the launch side:
-- this row deletes cleanly, but launch's matching down migration
-- (48_AddRDSEventTriggerType) is a no-op because Postgres cannot drop an enum
-- value. A full rollback across both services leaves the launch enum label in
-- place while the api seed row is gone -- expected and harmless (unused label).
DELETE FROM trigger_type WHERE name = 'rds-event';
