-- Remove the mqtt trigger_type seed row. Note the asymmetry with the launch
-- side: this seed row deletes cleanly, but launch's matching down migration
-- (41_AddMqttTriggerType) is a no-op because Postgres can't drop an enum value.
-- So a full rollback across both services leaves the launch enum value in place
-- while the api seed row is gone — expected, and harmless (the orphaned enum
-- label is simply unused).
DELETE FROM trigger_type WHERE name = 'mqtt';
