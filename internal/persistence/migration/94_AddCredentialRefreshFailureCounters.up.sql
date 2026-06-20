-- Tracks consecutive refresh failures per credential so the poller
-- can stop hammering a token that's gone permanently bad (refresh
-- token revoked, OAuth client mismatch, etc.) instead of retrying
-- every 60 seconds forever.
--
-- The poller already writes status='error' on transient failures.
-- This migration introduces a counter the poller increments on each
-- failure and zeros on success; once the count exceeds a threshold,
-- or the error itself is classified as permanent (invalid_grant,
-- unauthorized_client, invalid_client from Google's OAuth API), the
-- row transitions to status='revoked' and is excluded from future
-- refresh attempts. The user's natural recovery is re-running the
-- OAuth flow, which resets the counter via the existing upsert path.

ALTER TABLE agent_user_google_account
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;

ALTER TABLE trigger_google_account
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;

ALTER TABLE environment_credential
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
