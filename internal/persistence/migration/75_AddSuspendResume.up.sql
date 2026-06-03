-- Add suspended state to execution status enums
-- These must be in their own migration (no references to the new values)
ALTER TYPE ExecutionState ADD VALUE IF NOT EXISTS 'suspended';
ALTER TYPE CompletionState ADD VALUE IF NOT EXISTS 'suspended';
