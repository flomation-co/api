-- Consent evidence for marketing email.
--
-- users.marketing_opt_in on its own records the current state but not the
-- decision behind it, and UK GDPR Art 7(1) requires a controller to be able to
-- DEMONSTRATE that consent was given. These columns record when the decision
-- was made, through which surface, and against which wording.
--
-- marketing_consent_at is the moment of the most recent decision, whichever way
-- it went, so a withdrawal is evidenced as clearly as a grant. A NULL means the
-- user has never been asked, which is deliberately distinct from a recorded
-- refusal — the two must not be reported as the same thing.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS marketing_consent_at      TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS marketing_consent_source  TEXT,
    ADD COLUMN IF NOT EXISTS marketing_consent_version TEXT;

-- Existing opted-in users made a real choice in the welcome modal, but we did
-- not record when. Backfill from welcome_completed_at, which is the moment that
-- modal was submitted, and attribute it to that surface so the evidence is
-- honest about where it came from rather than claiming more precision than we
-- have. Users who never opted in are left NULL — we cannot tell from the data
-- whether they declined or were never asked.
UPDATE users
SET marketing_consent_at     = welcome_completed_at,
    marketing_consent_source = 'welcome_modal_backfill'
WHERE marketing_opt_in
  AND marketing_consent_at IS NULL
  AND welcome_completed_at IS NOT NULL;
