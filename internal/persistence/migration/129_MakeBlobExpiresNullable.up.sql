-- Flow assets (purpose='flo_asset', added for the editor file-upload feature)
-- are PERMANENT: they store expires_at = NULL and are reclaimed by an orphan
-- sweep, not a TTL. Make expires_at nullable so those inserts are accepted.
-- The GC sweep is unaffected: it already only touches rows WHERE
-- expires_at < NOW(), which never matches NULL.
ALTER TABLE blob_object ALTER COLUMN expires_at DROP NOT NULL;
