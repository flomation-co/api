-- R3 Phase 2 follow-up: identity channel_type "teams" → "microsoft".
--
-- The identity declaration model collapses Microsoft transports (Teams,
-- Outlook, future surfaces) onto a single "microsoft" channel because
-- they all share the same AAD Object ID. The inbound webhook resolver
-- normalises "teams" → "microsoft" at lookup time
-- (api/internal/agent/inbound.go normaliseChannelType), so any
-- pre-existing user_identity rows declared with the old "teams" value
-- must be renamed to match.
--
-- Idempotent: ON CONFLICT DO NOTHING in case a user has somehow
-- declared both "teams" and "microsoft" for the same external_id
-- (impossible via UI but cheap to guard).

UPDATE user_identity
   SET channel_type = 'microsoft'
 WHERE channel_type = 'teams';
