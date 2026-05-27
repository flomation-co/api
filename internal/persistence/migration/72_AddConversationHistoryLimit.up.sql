-- Add configurable conversation history limit per agent.
-- Defaults to 20 messages when not explicitly set.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS conversation_history_limit INTEGER NOT NULL DEFAULT 20;
