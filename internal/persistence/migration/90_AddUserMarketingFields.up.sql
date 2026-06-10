-- Welcome modal + EmailOctopus sync state.
--
-- Three columns added to users:
--
--   welcome_completed_at  TIMESTAMPTZ  When the user dismissed the
--                                       post-EULA welcome modal (set
--                                       display name + optional
--                                       marketing opt-in). NULL means
--                                       they haven't seen it yet — the
--                                       editor mounts the modal until
--                                       this is populated.
--
--   marketing_synced_at   TIMESTAMPTZ  Last successful EmailOctopus
--                                       sync (subscribe / update /
--                                       unsubscribe). Used by the
--                                       retry poller to decide whether
--                                       the user's current marketing
--                                       state has been pushed to EO.
--
--   marketing_sync_error  TEXT         Last failure reason from EO.
--                                       NULL means clean. Surfaced to
--                                       the retry poller which clears
--                                       it on success.
--
-- Existing marketing_opt_in BOOL column is reused; no schema change
-- needed for the flag itself.
--
-- Backfill semantics: existing rows have welcome_completed_at NULL by
-- default, so every existing user gets the welcome modal on next
-- login. This is intentional — gives current users a chance to opt
-- into marketing retroactively.

ALTER TABLE users
    ADD COLUMN welcome_completed_at TIMESTAMPTZ,
    ADD COLUMN marketing_synced_at  TIMESTAMPTZ,
    ADD COLUMN marketing_sync_error TEXT;

-- The retry poller scans this index to find rows that need re-sync.
-- Partial index keeps it tiny — only failed-sync rows are indexed.
CREATE INDEX IF NOT EXISTS idx_users_marketing_sync_error
    ON users(marketing_sync_error)
    WHERE marketing_sync_error IS NOT NULL;
