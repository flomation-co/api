-- Reverses 93_AddExecutionHierarchyMetadata. Migration 36's
-- parent_execution_id column and idx_execution_parent index are
-- left in place since they are not owned by this migration.

DROP INDEX IF EXISTS execution_roots_only_idx;
DROP INDEX IF EXISTS execution_root_created_idx;

ALTER TABLE execution
    DROP COLUMN IF EXISTS depth,
    DROP COLUMN IF EXISTS root_execution_id,
    DROP COLUMN IF EXISTS parent_metadata,
    DROP COLUMN IF EXISTS parent_relationship;
