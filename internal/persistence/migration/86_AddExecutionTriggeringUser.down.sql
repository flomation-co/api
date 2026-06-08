DROP INDEX IF EXISTS idx_execution_triggering_user;
ALTER TABLE execution DROP COLUMN IF EXISTS triggering_user_id;
