-- Revert: collapse "microsoft" back to "teams". Lossy if any non-Teams
-- Microsoft identities have been declared between the up and down
-- migrations; expected to be safe in practice since the up migration
-- ran before non-Teams Microsoft surfaces were added.

UPDATE user_identity
   SET channel_type = 'teams'
 WHERE channel_type = 'microsoft';
