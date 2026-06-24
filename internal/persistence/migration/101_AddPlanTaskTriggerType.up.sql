-- Agent Planning M1.5 fallout: the Plan Task Trigger node (executor
-- package actions/trigger/plan_task) syncs into the database as a
-- trigger row whose type column references trigger_type(id). When
-- the editor saves an orchestrator flow containing this node, the
-- API's flow-revision sync resolves the type via
--   (SELECT id FROM trigger_type WHERE name = :type_name)
-- and the package-name "plan_task" gets converted to "plan-task" by
-- the underscore-to-hyphen sync mangling. Without this row the
-- sub-SELECT returns NULL, and either the trigger insert fails
-- (NOT NULL violation) or — worse — silently leaves the row absent
-- so plan tick can't dispatch orchestrator-kind tasks.
--
-- ON CONFLICT DO NOTHING matches every other trigger_type seed in
-- the migration history (see 22, 37, 61, 70, 74, 81, 82). Idempotent
-- so re-running this migration after a partial deploy is safe.

INSERT INTO trigger_type (name) VALUES ('plan-task') ON CONFLICT DO NOTHING;
