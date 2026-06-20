-- Adds the hierarchy metadata columns on top of migration 36's
-- existing parent_execution_id. Together these support N-level
-- execution trees with O(1) root and ancestor queries.
--
-- Columns added:
--   parent_relationship  TEXT     classifier (e.g. 'plan_task', 'remote_trigger', 'subflow')
--   parent_metadata      JSONB    free-form context for the parent link
--   root_execution_id    UUID     non-null pointer to the top of the tree (self-ref for roots)
--   depth                INTEGER  cached distance from the root, capped at MaxDepth in app code
--
-- Note: parent_execution_id and idx_execution_parent already exist
-- from migration 36 and are intentionally left untouched.

ALTER TABLE execution
    ADD COLUMN parent_relationship TEXT,
    ADD COLUMN parent_metadata     JSONB,
    ADD COLUMN root_execution_id   UUID REFERENCES execution(id) ON DELETE SET NULL,
    ADD COLUMN depth               INTEGER NOT NULL DEFAULT 0;

-- Backfill root_execution_id and depth via a recursive walk from each
-- parentless row downward. In the OVH sandbox no row currently has
-- parent_execution_id set (no INSERT path writes it as of migration
-- 92), so this walk degenerates to "root_execution_id = id, depth = 0"
-- for every row. The CTE is here to stay correct if any row was ever
-- linked manually.
WITH RECURSIVE walk AS (
    SELECT id, parent_execution_id, id AS root_id, 0 AS d
    FROM execution
    WHERE parent_execution_id IS NULL
    UNION ALL
    SELECT e.id, e.parent_execution_id, w.root_id, w.d + 1
    FROM execution e
    JOIN walk w ON e.parent_execution_id = w.id
)
UPDATE execution e
SET root_execution_id = w.root_id,
    depth             = w.d
FROM walk w
WHERE w.id = e.id;

-- Defensive: any row not reached by the walk (would only happen if
-- parent_execution_id pointed at a row that no longer exists) becomes
-- its own root. Without this the SET NOT NULL below would fail.
UPDATE execution SET root_execution_id = id WHERE root_execution_id IS NULL;

ALTER TABLE execution ALTER COLUMN root_execution_id SET NOT NULL;

-- Tree fetch: SELECT ... WHERE root_execution_id = $1 ORDER BY depth, created_at
CREATE INDEX execution_root_created_idx
    ON execution (root_execution_id, created_at DESC);

-- Roots-only list: WHERE parent_execution_id IS NULL ORDER BY created_at DESC.
-- Agent executions are treated as legitimate roots here (no agent_id filter)
-- so plan-driven subtrees rendered under the agent root remain reachable
-- via this index.
CREATE INDEX execution_roots_only_idx
    ON execution (created_at DESC)
    WHERE parent_execution_id IS NULL;
