-- Extends the blob store's auth surface to support personal-mode
-- flows — agents and flows that aren't owned by an organisation.
--
-- Today blob_object.org_id is NOT NULL and FK'd to organisation(id),
-- which means personal-mode users can never store an inbound photo,
-- a tool-output blob, or anything else. The same applies to
-- blob_quota_daily.
--
-- The fix is a discriminated union at the row level: each blob row
-- carries EITHER an org_id (organisation-scoped) OR an owner_id
-- (user-scoped). The CHECK constraint enforces exactly-one. Lookups
-- match on whichever scope the caller carries; cross-scope reads
-- collapse to ErrBlobNotFound the same way cross-org reads always
-- have.

-- owner_id is an unconstrained UUID rather than an FK to a user
-- table — Sentinel owns user identity and the API references user
-- IDs by value only, the same pattern as agent.owner_id and
-- flo.author_id. A user hard-delete in Sentinel leaves orphan
-- rows here; the 30-day TTL sweep claims them eventually.
ALTER TABLE blob_object
    ALTER COLUMN org_id DROP NOT NULL,
    ADD COLUMN owner_id UUID,
    ADD CONSTRAINT blob_object_scope_exactly_one
        CHECK ((org_id IS NOT NULL AND owner_id IS NULL)
            OR (org_id IS NULL AND owner_id IS NOT NULL));

-- Personal-mode read path mirrors the org-mode index — used by
-- "WHERE handle = $1 AND owner_id = $2" on every GET / DELETE.
CREATE INDEX blob_object_owner_handle_idx
    ON blob_object (owner_id, handle)
    WHERE owner_id IS NOT NULL;

-- Per-org index is left alone — it still serves the existing
-- org-mode path. The original blob_object_org_handle_idx covers
-- (org_id, handle) and works unchanged.

-- Quota table follows the same discriminated-union shape. ORDER
-- MATTERS here: org_id is part of blob_quota_daily's composite PK
-- (from migration 96), and Postgres won't let us drop its NOT NULL
-- while it's still in the PK. So the steps must be: (1) drop the
-- PK, (2) drop NOT NULL on org_id, (3) add owner_id, (4) add the
-- discriminated-union CHECK, (5) create the new unique index that
-- replaces the PK's uniqueness guarantee.
ALTER TABLE blob_quota_daily
    DROP CONSTRAINT blob_quota_daily_pkey;

ALTER TABLE blob_quota_daily
    ALTER COLUMN org_id DROP NOT NULL,
    ADD COLUMN owner_id UUID,
    ADD CONSTRAINT blob_quota_daily_scope_exactly_one
        CHECK ((org_id IS NOT NULL AND owner_id IS NULL)
            OR (org_id IS NULL AND owner_id IS NOT NULL));

-- The new unique index replaces the dropped PK's uniqueness
-- guarantee. COALESCE'ing NULL to '' keeps the index well-defined
-- across the discriminated union (Postgres treats NULL keys as
-- distinct, which would let two rows for the same scope+day collide
-- silently).
CREATE UNIQUE INDEX blob_quota_daily_scope_day_idx
    ON blob_quota_daily (
        COALESCE(org_id::text, ''),
        COALESCE(owner_id::text, ''),
        quota_day
    );
