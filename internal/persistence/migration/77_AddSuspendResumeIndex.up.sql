-- Index for timed resume polling.
-- Uses a plain partial index on resume_at (not filtered by enum value)
-- because ALTER TYPE ADD VALUE must be committed before the value can
-- be referenced, and golang-migrate runs all pending migrations in one
-- transaction.
CREATE INDEX IF NOT EXISTS idx_execution_resume_at ON execution(resume_at)
    WHERE resume_at IS NOT NULL;
