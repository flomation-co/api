-- The name column is a display label, not an identifier. Multiple actions
-- across different categories can share the same human-readable name
-- (e.g. "Send Email" in both messaging and microsoft/outlook).
ALTER TABLE actions DROP CONSTRAINT IF EXISTS actions_name_key;
