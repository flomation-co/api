-- Restore the NOT NULL constraint. Any permanent (NULL) asset rows would block
-- it, so first give them a far-future expiry — the down-migration must not fail
-- on data the up-migration made valid.
UPDATE blob_object SET expires_at = NOW() + INTERVAL '100 years' WHERE expires_at IS NULL;
ALTER TABLE blob_object ALTER COLUMN expires_at SET NOT NULL;
