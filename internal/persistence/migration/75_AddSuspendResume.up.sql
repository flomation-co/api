-- Add suspended state to execution status enums
ALTER TYPE ExecutionState ADD VALUE IF NOT EXISTS 'suspended';
ALTER TYPE CompletionState ADD VALUE IF NOT EXISTS 'suspended';

-- Checkpoint data for resuming suspended executions
ALTER TABLE execution ADD COLUMN IF NOT EXISTS checkpoint JSONB DEFAULT NULL;

-- Resume scheduling
ALTER TABLE execution ADD COLUMN IF NOT EXISTS resume_at TIMESTAMPTZ DEFAULT NULL;

-- Event-based resume matching
ALTER TABLE execution ADD COLUMN IF NOT EXISTS resume_trigger_type VARCHAR DEFAULT NULL;
ALTER TABLE execution ADD COLUMN IF NOT EXISTS resume_trigger_match JSONB DEFAULT NULL;

-- Track suspend/resume cycles
ALTER TABLE execution ADD COLUMN IF NOT EXISTS suspend_count INT DEFAULT 0;

-- Execution segments: timing for each run/resume cycle
ALTER TABLE execution ADD COLUMN IF NOT EXISTS segments JSONB DEFAULT '[]'::jsonb;

-- Index for timed resume polling
CREATE INDEX IF NOT EXISTS idx_execution_resume_at ON execution(resume_at)
    WHERE execution_status = 'suspended' AND resume_at IS NOT NULL;
