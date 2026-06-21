DROP INDEX IF EXISTS blob_quota_daily_scope_day_idx;

ALTER TABLE blob_quota_daily
    DROP CONSTRAINT IF EXISTS blob_quota_daily_scope_exactly_one;

-- Restore the original (org_id, quota_day) PK. Personal-mode rows
-- must be cleared first or the PK rebuild will fail.
DELETE FROM blob_quota_daily WHERE owner_id IS NOT NULL;
ALTER TABLE blob_quota_daily
    DROP COLUMN owner_id,
    ALTER COLUMN org_id SET NOT NULL,
    ADD PRIMARY KEY (org_id, quota_day);

DROP INDEX IF EXISTS blob_object_owner_handle_idx;
ALTER TABLE blob_object
    DROP CONSTRAINT IF EXISTS blob_object_scope_exactly_one;

DELETE FROM blob_object WHERE owner_id IS NOT NULL;
ALTER TABLE blob_object
    DROP COLUMN owner_id,
    ALTER COLUMN org_id SET NOT NULL;
