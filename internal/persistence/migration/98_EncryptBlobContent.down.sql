-- Reverting requires decrypting before the rename. Without the
-- runtime key (only the application has it) this can't be done
-- inside a static migration — so down() takes the simpler path of
-- truncating + renaming back. Operators rolling back should accept
-- data loss on blob_object; no migration is willing to silently
-- expose previously-encrypted bytes by reverting the column name
-- without re-encryption.

TRUNCATE blob_object;
TRUNCATE blob_quota_daily;

ALTER TABLE blob_object RENAME COLUMN content_enc TO content;
