-- Reverting reintroduces the global-name constraint. If any orgs have
-- since created queues whose name collides with another org's, the
-- ADD CONSTRAINT will fail — operator must dedupe first.

DROP INDEX IF EXISTS idx_queue_org_name;
ALTER TABLE queue ADD CONSTRAINT queue_name_key UNIQUE (name);
