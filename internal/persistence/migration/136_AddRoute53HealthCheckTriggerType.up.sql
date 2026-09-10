INSERT INTO trigger_type (name) SELECT 'route53-health-check' WHERE NOT EXISTS (SELECT 1 FROM trigger_type t WHERE t.name = 'route53-health-check');
