-- Reverting M1.5 destroys orchestrator-kind rows. M1's schema
-- requires flow_id NOT NULL, and orchestrator-kind rows have it as
-- NULL by definition. Operators rolling back accept this data loss
-- explicitly; an in-place backfill would have to fabricate flow_ids
-- that don't represent the actual dispatch path.

DELETE FROM plan_task WHERE task_kind = 'orchestrator';

DROP INDEX IF EXISTS plan_task_kind_idx;

ALTER TABLE plan_task
    DROP CONSTRAINT IF EXISTS plan_task_kind_shape,
    DROP COLUMN task_kind,
    ALTER COLUMN flow_id SET NOT NULL,
    ALTER COLUMN flow_revision_id SET NOT NULL;
