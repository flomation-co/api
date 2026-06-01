DROP INDEX IF EXISTS idx_execution_resume_at;
ALTER TABLE execution DROP COLUMN IF EXISTS segments;
ALTER TABLE execution DROP COLUMN IF EXISTS suspend_count;
ALTER TABLE execution DROP COLUMN IF EXISTS resume_trigger_match;
ALTER TABLE execution DROP COLUMN IF EXISTS resume_trigger_type;
ALTER TABLE execution DROP COLUMN IF EXISTS resume_at;
ALTER TABLE execution DROP COLUMN IF EXISTS checkpoint;
