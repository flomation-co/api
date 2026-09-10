INSERT INTO trigger_type (name) SELECT 'cloudwatch-alarm'  WHERE NOT EXISTS (SELECT 1 FROM trigger_type t WHERE t.name = 'cloudwatch-alarm');
INSERT INTO trigger_type (name) SELECT 'cloudwatch-metric' WHERE NOT EXISTS (SELECT 1 FROM trigger_type t WHERE t.name = 'cloudwatch-metric');
INSERT INTO trigger_type (name) SELECT 'cloudwatch-logs'   WHERE NOT EXISTS (SELECT 1 FROM trigger_type t WHERE t.name = 'cloudwatch-logs');
