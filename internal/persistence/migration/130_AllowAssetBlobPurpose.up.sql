-- Allow the permanent flow-asset purpose (editor file uploads) in the purpose
-- CHECK. Without this an upload with purpose='flo_asset' is rejected by
-- blob_object_purpose_valid, which was created (migration 96) with only the
-- original three TTL'd purposes.
ALTER TABLE blob_object DROP CONSTRAINT blob_object_purpose_valid;
ALTER TABLE blob_object ADD CONSTRAINT blob_object_purpose_valid
    CHECK (purpose IN ('inbound', 'tool_output', 'manual', 'flo_asset'));
