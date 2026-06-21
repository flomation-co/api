-- Adds the three tables underpinning Agent Planning M1: plan,
-- plan_task, plan_event. See plans/agent_planning.md and
-- plans/agent_planning_m1.md for the full design.
--
-- Plans are AI-authored at runtime and progressed by an orchestrator
-- that fires their tasks as ordinary flow executions (each linked
-- back via execution.parent_execution_id with
-- parent_relationship='plan_task' — that relationship value was
-- reserved in migration 93 ahead of this work).
--
-- Two foreign-key design notes worth being explicit about:
--
--  * owner_user_id is an unconstrained UUID rather than an FK to a
--    user table. Sentinel owns user identity in this codebase and
--    the API references user IDs by value only — the same pattern
--    agent.owner_id and flo.author_id already use. A user hard-
--    delete in Sentinel will leave orphan plan rows here; a
--    follow-up GC sweep (M5) will handle that.
--
--  * flow_revision_id is also unconstrained. flo_revision exists as
--    a table but its lifecycle (revisions can be soft-deleted) and
--    cross-environment behaviour make a hard FK fragile. The
--    plan/create handler validates existence at insert time; we
--    don't want CASCADE/SET NULL surprises mutating a plan's task
--    pinning later.

CREATE TABLE plan (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id                 UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    owner_user_id            UUID,
    organisation_id          UUID REFERENCES organisation(id) ON DELETE CASCADE,
    created_by_execution_id  UUID REFERENCES execution(id) ON DELETE SET NULL,
    title                    TEXT NOT NULL,
    goal                     TEXT NOT NULL,
    status                   TEXT NOT NULL DEFAULT 'draft'
                             CHECK (status IN ('draft', 'active', 'blocked', 'completed', 'cancelled')),
    -- next_check_at drives the Launch-side tick poller. NULL means
    -- "tick on the next scan"; a future timestamp means "wait until
    -- then" (used for tasks with not_before set).
    next_check_at            TIMESTAMPTZ,
    suspend_count            INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at             TIMESTAMPTZ,
    cancelled_at             TIMESTAMPTZ,
    cancelled_reason         TEXT
);

CREATE INDEX plan_org_agent_idx
    ON plan (organisation_id, agent_id);

CREATE INDEX plan_owner_agent_idx
    ON plan (owner_user_id, agent_id);

CREATE INDEX plan_created_by_execution_idx
    ON plan (created_by_execution_id);

-- Tick poller's primary query: "active plans whose next_check_at is
-- now or in the past." NULLS FIRST so a fresh draft-transitioned-to-
-- active plan gets picked up immediately.
CREATE INDEX plan_ready_tick_idx
    ON plan (next_check_at NULLS FIRST)
    WHERE status = 'active';


CREATE TABLE plan_task (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id             UUID NOT NULL REFERENCES plan(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    description         TEXT,
    flow_id             UUID NOT NULL REFERENCES flo(id) ON DELETE RESTRICT,
    -- Pin to a specific revision so a flow edit mid-plan can't
    -- silently change a task's behaviour. The pin is validated at
    -- plan/create time, not FK-enforced (see file header note).
    flow_revision_id    UUID NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'ready', 'in_progress', 'completed', 'failed', 'cancelled')),
    -- depends_on is an array of OTHER plan_task UUIDs in the same
    -- plan. GIN-indexed below so the tick endpoint's "find tasks
    -- whose dependencies are all complete" query stays cheap.
    depends_on          UUID[] NOT NULL DEFAULT '{}',
    -- not_before is the earliest time this task can be dispatched
    -- regardless of dependency status — used for time-gated tasks
    -- ("send the reminder at 4 PM tomorrow").
    not_before          TIMESTAMPTZ,
    inputs_json         JSONB NOT NULL DEFAULT '{}'::jsonb,
    outputs_json        JSONB,
    -- execution_id is populated when the task is dispatched; the
    -- completion writeback (M1 commit 6) uses it to find the task
    -- on UpdateCompletionStatus and mark the task done.
    execution_id        UUID,
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    max_attempts        INTEGER NOT NULL DEFAULT 1,
    timeout_seconds     INTEGER,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    -- Task names are scoped per-plan and used by the variable
    -- substitution helper (M1 commit 3) — references like
    -- ${task.gather_data.output.results} resolve via name.
    UNIQUE (plan_id, name)
);

CREATE INDEX plan_task_plan_status_idx
    ON plan_task (plan_id, status);

CREATE INDEX plan_task_execution_idx
    ON plan_task (execution_id)
    WHERE execution_id IS NOT NULL;

CREATE INDEX plan_task_depends_on_gin
    ON plan_task USING GIN (depends_on);


CREATE TABLE plan_event (
    id                  BIGSERIAL PRIMARY KEY,
    plan_id             UUID NOT NULL REFERENCES plan(id) ON DELETE CASCADE,
    plan_task_id        UUID REFERENCES plan_task(id) ON DELETE CASCADE,
    event_type          TEXT NOT NULL,
    data                JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Plan-event timeline view: "show me everything that happened on
-- this plan, newest first."
CREATE INDEX plan_event_plan_timeline_idx
    ON plan_event (plan_id, created_at DESC);
