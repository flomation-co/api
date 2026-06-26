package persistence

import (
	"flomation.app/automate/api"
)

// getMicrosoftAccountsCrossChannelSQL is the parallel of
// getGoogleAccountsCrossChannelSQL for the microsoft_account table
// (migration 78, with tokens encrypted at rest in migration 102).
// Walks the same identity graph — agent_user → declared channel
// identities → Flomation user → sibling channels → sibling agent_users
// → their Microsoft accounts — so a user who linked Outlook on one
// channel is recognised on all the channels they've declared in their
// profile.
//
// One shape difference from the Google version: the table uses `email`
// rather than `google_email`. Encryption model is identical: tokens
// are BYTEA with PGP_SYM_ENCRYPT on write and PGP_SYM_DECRYPT on read,
// keyed by config.Database.EncryptionKey (parameter $2).
//
// No production call site reads from this function yet — when Outlook
// or Teams agent actions land they'll route through here in the same
// way calendar/gmail/drive route through the Google equivalent. The
// function exists in this PR so the bug we fixed for Google can't
// silently regress when Microsoft actions are wired up.
const getMicrosoftAccountsCrossChannelSQL = `
WITH source_handles AS (
    SELECT channel_type, channel_external_id
    FROM agent_identity
    WHERE agent_user_id = $1
),
source_agent AS (
    SELECT agent_id FROM agent_user WHERE id = $1
),
declared_user AS (
    SELECT DISTINCT ui.user_id, ui.organisation_id
    FROM user_identity ui
    JOIN source_handles sh
      ON ui.channel_type = sh.channel_type
     AND ui.external_id  = sh.channel_external_id
),
sibling_handles AS (
    SELECT ui.channel_type, ui.external_id
    FROM user_identity ui
    JOIN declared_user du
      ON ui.user_id        = du.user_id
     AND ui.organisation_id = du.organisation_id
),
sibling_users AS (
    SELECT DISTINCT ai.agent_user_id
    FROM agent_identity ai
    JOIN agent_user au ON au.id = ai.agent_user_id
    JOIN source_agent sa ON sa.agent_id = au.agent_id
    JOIN sibling_handles sh
      ON ai.channel_type        = sh.channel_type
     AND ai.channel_external_id = sh.external_id
    UNION
    SELECT $1::UUID
)
SELECT id, agent_user_id, email, label, purpose,
       PGP_SYM_DECRYPT(access_token,  $2) AS access_token,
       PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
       token_expires_at, status, last_error,
       created_at, updated_at
FROM microsoft_account
WHERE agent_user_id IN (SELECT agent_user_id FROM sibling_users)
`

// GetMicrosoftAccountsForLinkedUsers returns every connected Microsoft
// (Outlook/Teams) account the requesting agent_user can reach,
// including accounts linked to a sibling agent_user that represents
// the SAME Flomation user via user_identity declarations.
//
// Mirror of GetGoogleAccountsForLinkedUsers — see that function's
// docstring for the design discussion. Backwards compatible by
// construction: the sibling_users CTE always UNION's the requesting
// agent_user_id, so a user with no user_identity declarations gets
// exactly the legacy "only my own accounts" behaviour.
func (s *Service) GetMicrosoftAccountsForLinkedUsers(agentUserID string, purpose ...string) ([]*api.MicrosoftAccount, error) {
	var results []*api.MicrosoftAccount
	var err error

	if len(purpose) > 0 && purpose[0] != "" {
		err = s.conn.Select(&results,
			getMicrosoftAccountsCrossChannelSQL+` AND purpose = $3 ORDER BY created_at ASC`,
			agentUserID, s.config.Database.EncryptionKey, purpose[0])
	} else {
		err = s.conn.Select(&results,
			getMicrosoftAccountsCrossChannelSQL+` ORDER BY created_at ASC`,
			agentUserID, s.config.Database.EncryptionKey)
	}

	if err != nil {
		return nil, err
	}
	return results, nil
}
