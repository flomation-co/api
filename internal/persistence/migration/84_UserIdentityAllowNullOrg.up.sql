-- R3 Phase 1.5: Allow user_identity declarations in personal mode.
--
-- Migration 83 made user_identity.organisation_id NOT NULL because every
-- declaration was assumed to be inside an organisation. In practice users
-- also run personal agents and need to be recognised there too — so we
-- relax the column to nullable and represent personal declarations with
-- NULL organisation_id.
--
-- The PRIMARY KEY can't span a nullable column, so we replace it with a
-- functional UNIQUE INDEX that collapses NULL to a sentinel so the unique
-- constraint behaves as expected ("the same user can't declare the same
-- (channel, external_id) twice in personal mode either").

ALTER TABLE user_identity
    DROP CONSTRAINT user_identity_pkey,
    ALTER COLUMN organisation_id DROP NOT NULL;

CREATE UNIQUE INDEX user_identity_uniq_idx
    ON user_identity (
        user_id,
        COALESCE(organisation_id::text, ''),
        channel_type,
        external_id
    );
