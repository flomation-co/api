-- Migration 49: Add notified_at to agent_pending_action
-- The pending action poller in Launch dispatches confirmation prompts
-- proactively. notified_at tracks whether a prompt has been sent so
-- the poller doesn't re-fire on every poll cycle.
ALTER TABLE agent_pending_action ADD COLUMN IF NOT EXISTS notified_at TIMESTAMPTZ;