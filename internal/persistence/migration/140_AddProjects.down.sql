DROP INDEX IF EXISTS idx_flo_project;
ALTER TABLE flo DROP COLUMN IF EXISTS project_id;
DROP INDEX IF EXISTS idx_project_owner;
DROP INDEX IF EXISTS idx_project_org;
DROP INDEX IF EXISTS idx_project_parent;
DROP TABLE IF EXISTS project;
