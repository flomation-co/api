-- Reverse: re-add the PK, drop the functional unique index. This will
-- fail if any rows have NULL organisation_id — operator must either
-- delete or assign an org to those rows before downgrading.
DROP INDEX IF EXISTS user_identity_uniq_idx;

ALTER TABLE user_identity
    ALTER COLUMN organisation_id SET NOT NULL,
    ADD PRIMARY KEY (user_id, organisation_id, channel_type, external_id);
