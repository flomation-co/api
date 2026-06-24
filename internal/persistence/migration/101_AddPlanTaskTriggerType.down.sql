-- Reverse migration 101. Note: this DELETE will fail with an FK
-- violation if any trigger row currently references this
-- trigger_type id (i.e. any orchestrator flow has saved a Plan Task
-- Trigger node). That's the intended posture for a rollback —
-- downgrading the API past M1.5 requires first removing the trigger
-- rows that depend on this type, which is a deliberate manual step.

DELETE FROM trigger_type WHERE name = 'plan-task';
