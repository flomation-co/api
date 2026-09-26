-- A personal account's Data Processing Agreement now exists from the moment
-- the account does, rather than coming into being the first time someone
-- clicks download.
--
-- Why this needs storing at all, when the PDF is still generated fresh every
-- time: the agreement's EFFECTIVE DATE was time.Now() at download, so the
-- contract re-dated itself on every download and there was no record of when
-- the Article 28 arrangement actually began. The document stays generated on
-- demand; only the facts that cannot be re-derived are kept.
--
-- Mirrors the eula_version / eula_accepted_at pair on this same table.
ALTER TABLE users ADD COLUMN IF NOT EXISTS dpa_effective_from   TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS dpa_template_version TEXT;

-- Existing accounts get the date they registered. That is the honest effective
-- date for an agreement that is meant to exist from registration.
--
-- template_version stays NULL for them on purpose: we genuinely do not know
-- which template was current when they signed up, and stamping today's version
-- on a backdated agreement would be a fabrication. NULL reads as "predates
-- version tracking", which is true.
UPDATE users
   SET dpa_effective_from = created_at
 WHERE dpa_effective_from IS NULL;
