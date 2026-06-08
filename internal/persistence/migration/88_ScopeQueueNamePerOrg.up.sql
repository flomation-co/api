-- Scope queue.name uniqueness per-organisation.
--
-- The original constraint in migration 11 declared `name VARCHAR UNIQUE`
-- as a column-level constraint (auto-named queue_name_key by Postgres),
-- making queue names globally unique across the entire database. This
-- blocks multi-tenancy: two organisations cannot both create a queue
-- called "Default Queue", "Production", or any other natural name —
-- the second org's INSERT fails.
--
-- No code path looks up queues by name (the only WHERE clause in
-- persistence/service.go is by organisation_id; the INSERT is
-- (organisation_id, parent_id, name)). The global unique was vestigial.
--
-- New constraint: per-(organisation_id, name). Postgres treats NULL as
-- distinct in unique indexes, so multiple platform-wide queues
-- (organisation_id IS NULL) can also share names — fine in practice
-- because the only NULL-org queue is the seed "Default Queue" created
-- in migration 11. If a stricter no-duplicate-NULL-org rule is needed
-- later, replace this with a functional index using COALESCE.

ALTER TABLE queue DROP CONSTRAINT IF EXISTS queue_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_queue_org_name ON queue(organisation_id, name);
