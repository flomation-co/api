-- Migration 148 backfilled consent evidence from welcome_completed_at, which
-- only reaches users who completed the welcome modal. A few accounts carry
-- marketing_opt_in = true without ever having completed it — the flag was set
-- some other way, such as the profile toggle.
--
-- Those rows have no evidence at all, and because the marketing sync now
-- qualifies rows on welcome_completed_at OR marketing_consent_at, they also
-- have no route to EmailOctopus. Their stated preference would be silently
-- ignored: on live this was 2 of the 11 opted-in users.
--
-- Stamp them from created_at, with a source that says plainly the date is NOT
-- the date of consent. This is deliberately weak evidence, named as such — if
-- consent for these accounts ever has to be demonstrated under UK GDPR
-- Art 7(1), the honest position is to re-ask rather than rely on this row.
UPDATE users
SET marketing_consent_at     = created_at,
    marketing_consent_source = 'legacy_opt_in_unknown_date'
WHERE marketing_opt_in
  AND marketing_consent_at IS NULL;
