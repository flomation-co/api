-- Index for timed resume polling (references the 'suspended' enum value
-- which was added in migration 75 and must be committed first)
CREATE INDEX IF NOT EXISTS idx_execution_resume_at ON execution(resume_at)
    WHERE execution_status = 'suspended' AND resume_at IS NOT NULL;
