-- Track who triggered each execution at the platform-user level.
--
-- Set by both the agent inbound pipeline AND the unified trigger
-- dispatch path, so every execution — agent or standalone — records
-- the resolved user (declared identity or anonymous stub) responsible
-- for firing it. The Executions table UI surfaces this as the new
-- "Triggered by" column.

ALTER TABLE execution
    ADD COLUMN triggering_user_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_execution_triggering_user
    ON execution(triggering_user_id);
