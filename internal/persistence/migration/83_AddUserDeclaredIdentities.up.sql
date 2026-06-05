-- R2.1: User-declared channel identities (foundation).
--
-- Moves channel-identity declaration away from the AI-initiated
-- [LINK_OFFER] flow and onto user profile settings, with per-organisation
-- scoping. Existing agent_user / agent_identity stays in place — this
-- migration is purely additive so the legacy identity flow keeps working
-- through the rollout window.
--
-- Adds:
--   1. is_anonymous + per-org channel-identity columns on `users`, so
--      a message from an unrecognised channel identity creates a stub
--      user row uniquely keyed per receiving organisation (preventing
--      cross-org inference of the same external channel ID).
--   2. user_identity table — declared (user-confirmed) mappings from a
--      Flomation user to their channel handle within a specific org.
--      Looked up by webhook ingestion via (org_id, channel_type, ext_id).

ALTER TABLE users
    ADD COLUMN is_anonymous        BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN organisation_id     UUID    REFERENCES organisation(id) ON DELETE CASCADE,
    ADD COLUMN channel_type        TEXT,
    ADD COLUMN channel_external_id TEXT;

-- Defensive invariant: anonymous rows must carry their channel identity.
ALTER TABLE users ADD CONSTRAINT users_anon_has_channel_identity CHECK (
    is_anonymous = false OR (
        organisation_id     IS NOT NULL
        AND channel_type        IS NOT NULL
        AND channel_external_id IS NOT NULL
    )
);

-- Per-org uniqueness on anonymous rows: the same Slack user messaging
-- two different orgs creates two distinct anonymous user rows. This is
-- the privacy-preserving choice — otherwise org A could infer their
-- members talk to org B via shared user_id.
CREATE UNIQUE INDEX users_anonymous_channel_idx
    ON users (organisation_id, channel_type, channel_external_id)
    WHERE is_anonymous = true;

-- Declared channel identities for Flomation users, organisation-scoped.
-- The webhook ingestion path resolves an incoming sender by looking up
-- (organisation_id, channel_type, external_id) -> user_id here first;
-- on miss it falls through to the anonymous-user upsert above.
CREATE TABLE user_identity (
    user_id          UUID        NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    organisation_id  UUID        NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    channel_type     TEXT        NOT NULL,
    external_id      TEXT        NOT NULL,
    display_name     TEXT,
    verified_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, organisation_id, channel_type, external_id)
);

-- Secondary index optimised for the webhook lookup direction
-- (org + channel + external_id are known; user_id is the result).
CREATE INDEX user_identity_lookup_idx
    ON user_identity (organisation_id, channel_type, external_id);
