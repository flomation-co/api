-- ActionTypeAwait (7) — the Human-in-the-Loop node. Mirrors migration 38,
-- which added '6' for Switch.
ALTER TYPE action_type ADD VALUE IF NOT EXISTS '7';
