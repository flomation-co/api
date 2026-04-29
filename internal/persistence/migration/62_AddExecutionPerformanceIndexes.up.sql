CREATE INDEX IF NOT EXISTS idx_execution_owner_created
    ON execution (owner_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_execution_org_created
    ON execution (organisation_id, created_at DESC)
    WHERE organisation_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_execution_flo_created
    ON execution (flo_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_trigger_invocation_trigger
    ON trigger_invocation (trigger_id);
