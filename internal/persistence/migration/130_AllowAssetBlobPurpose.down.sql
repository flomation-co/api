-- Revert to the original three purposes. Any flo_asset rows would violate the
-- restored constraint, so delete them first — a schema without the purpose
-- cannot retain rows that use it.
DELETE FROM blob_object WHERE purpose = 'flo_asset';
ALTER TABLE blob_object DROP CONSTRAINT blob_object_purpose_valid;
ALTER TABLE blob_object ADD CONSTRAINT blob_object_purpose_valid
    CHECK (purpose IN ('inbound', 'tool_output', 'manual'));
