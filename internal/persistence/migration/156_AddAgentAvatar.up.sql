-- A small square image shown wherever the agent is listed. Held as a
-- data URL rather than a blob so it needs no second request: an <img>
-- in the editor cannot carry the Bearer token a binary endpoint would
-- demand. The editor downscales before upload and the API caps the
-- size, so the column stays in the low tens of kilobytes.
ALTER TABLE agent ADD COLUMN avatar TEXT;
