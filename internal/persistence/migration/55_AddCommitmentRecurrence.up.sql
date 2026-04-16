-- Add recurrence support for recurring commitments (daily, weekly, monthly, cron).
-- NULL = one-off (current behaviour). Non-null = recurring.
ALTER TABLE agent_commitment ADD COLUMN IF NOT EXISTS recurrence VARCHAR(100);
