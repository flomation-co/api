-- Per-(user, organisation) checklist progress.
--
-- The Getting Started checklist used to be a single bitmask on
-- users.checklist_flags. That conflated two kinds of progress:
--
--   * Properties of the human ("I've set my profile name", "I've
--     enabled MFA") — these are global and shouldn't reset when the
--     user switches between Personal mode and an organisation.
--
--   * Properties of the work the user has done in a specific
--     context ("I've created a flow", "I've executed a flow", "I've
--     configured an environment", "I've invited my team") — these
--     are org-scoped. A user who's built flows in Personal mode
--     shouldn't see those ticked when they join a brand-new org
--     where they haven't done anything yet.
--
-- Migration plan:
--
--   * Global bits (1 = profile_name, 32 = enable_mfa) stay on
--     users.checklist_flags. No change required for those.
--
--   * Org-scoped bits (2|4|8|16 = 30) move into this new table
--     keyed by (user_id, organisation_id). NULL organisation_id is
--     Personal mode.
--
--   * Backfill: existing users have all their progress on
--     users.checklist_flags; we copy the org-scoped bits to
--     (user_id, NULL, ...) so their personal-mode checklist still
--     reflects what they've achieved. We deliberately do NOT clear
--     those bits from users.checklist_flags here — that lets old
--     editor builds continue to display reasonable state during the
--     rollout window. A follow-up migration can clear the org-scoped
--     bits from the global column once the new editor is universally
--     deployed.
--
-- Why two partial unique indexes instead of a composite primary key:
--
--   PostgreSQL implicitly makes PRIMARY KEY columns NOT NULL, which
--   collides with the requirement that organisation_id be nullable
--   to represent Personal mode. We use a surrogate UUID primary key
--   and enforce uniqueness via two partial indexes — one per scope.
--   IS NOT DISTINCT FROM-style upserts in the persistence layer
--   target each index explicitly via ON CONFLICT clauses.

CREATE TABLE IF NOT EXISTS user_checklist_state (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organisation_id UUID REFERENCES organisation(id) ON DELETE CASCADE,
    flags           INTEGER NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One row per user in Personal mode (organisation_id IS NULL).
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_checklist_state_personal
    ON user_checklist_state(user_id)
    WHERE organisation_id IS NULL;

-- One row per (user, org) in org mode.
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_checklist_state_org
    ON user_checklist_state(user_id, organisation_id)
    WHERE organisation_id IS NOT NULL;

-- Lookup index for the read path (org switch refreshes the widget
-- via a single-row read; partial indexes above already handle Personal
-- and org-mode lookups, this is for completeness when querying by
-- user alone).
CREATE INDEX IF NOT EXISTS idx_user_checklist_state_user
    ON user_checklist_state(user_id);

-- Backfill personal-mode rows from existing users.checklist_flags,
-- masking down to just the org-scoped bits (30 = 2|4|8|16). Skip
-- users whose org-scoped bits are 0 so the table stays small.
INSERT INTO user_checklist_state (user_id, organisation_id, flags)
SELECT id, NULL, checklist_flags & 30
FROM users
WHERE (checklist_flags & 30) <> 0
ON CONFLICT DO NOTHING;
