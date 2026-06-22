-- Discriminated-union task model for plans. M1 (migration 99) required
-- every plan task to pin a (flow_id, flow_revision_id) — meaning the
-- agent had to author or curate a flow for every task it wanted in a
-- plan, even trivial ones. M1.5 flips the default: a task is now an
-- orchestrator invocation by default, with flow_id + flow_revision_id
-- reserved as an explicit override for tasks that need determinism.
--
-- See plans/agent_planning_m1_5.md for the full design rationale.
--
-- Schema shape:
--
--   task_kind ∈ {'orchestrator', 'flow'}
--
--     orchestrator (default):
--       flow_id IS NULL AND flow_revision_id IS NULL
--       At dispatch time the tick endpoint fires the agent's
--       orchestrator_flow_id via the new Plan Task Trigger node,
--       carrying task context as trigger data.
--
--     flow (explicit override):
--       flow_id IS NOT NULL AND flow_revision_id IS NOT NULL
--       Existing M1 behaviour — pin a curated flow for the task.
--
-- The CHECK constraint enforces "exactly one shape" at the row
-- level so the persistence layer can't accidentally produce a
-- malformed row even when the application logic drifts.

ALTER TABLE plan_task
    ALTER COLUMN flow_id DROP NOT NULL,
    ALTER COLUMN flow_revision_id DROP NOT NULL,
    ADD COLUMN task_kind TEXT NOT NULL DEFAULT 'orchestrator'
        CHECK (task_kind IN ('orchestrator', 'flow')),
    ADD CONSTRAINT plan_task_kind_shape CHECK (
        (task_kind = 'orchestrator'
            AND flow_id IS NULL AND flow_revision_id IS NULL)
        OR
        (task_kind = 'flow'
            AND flow_id IS NOT NULL AND flow_revision_id IS NOT NULL)
    );

-- Backfill any M1-era rows currently using flow-kind dispatch. They
-- have flow_id NOT NULL by the M1 schema invariant, so the partition
-- predicate matches them all.
UPDATE plan_task
SET task_kind = 'flow'
WHERE flow_id IS NOT NULL;

-- Narrow index for "how many tasks of kind X exist on this plan?" —
-- not in the hot tick path (which scans on plan_id + status), but
-- useful for the executions UI grouping and for ops queries.
CREATE INDEX plan_task_kind_idx ON plan_task(task_kind);
