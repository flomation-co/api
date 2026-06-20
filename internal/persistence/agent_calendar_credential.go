package persistence

// Calendar-credential lookup for the schedule-context feature.
//
// The system prompt assembler needs a Google Calendar access token
// to fetch upcoming events for a given agent_user. This file
// provides the single-purpose query — keep it separate from the
// general account CRUD so the schedule-context feature can be
// rolled back without touching unrelated code.
//
// The lookup is *narrowly scoped*:
//   - Only an account with purpose='calendar' is considered.
//   - status='revoked' rows are excluded (the refresh poller has
//     already given up on them).
//   - Only the latest non-expired access_token is returned. If the
//     access token is missing or stale, the caller skips the fetch
//     entirely and the schedule section is omitted from this turn's
//     system prompt — the refresh poller will rotate the token on
//     its next 60s tick.

import (
	"database/sql"
	"errors"
)

// GetAgentUserCalendarAccessToken returns a live (non-expired)
// Google Calendar access token for the given agent_user. Returns
// ("", nil) when:
//
//   - The user hasn't connected a calendar account (no row with
//     purpose='calendar').
//   - The account exists but has been revoked.
//   - The access_token is missing or expired (the refresh poller
//     will rotate it within ~60 s; the assembler simply skips the
//     section this turn rather than blocking on a refresh).
//
// Errors only on actual DB failures, not on "no token available".
func (s *Service) GetAgentUserCalendarAccessToken(agentUserID string) (string, error) {
	if agentUserID == "" {
		return "", nil
	}
	var token string
	err := s.conn.Get(&token, `
		SELECT COALESCE(PGP_SYM_DECRYPT(access_token, $2), '')
		FROM agent_user_google_account
		WHERE agent_user_id   = $1
		  AND purpose         = 'calendar'
		  AND access_token IS NOT NULL
		  AND token_expires_at > NOW()
		  AND status          = 'active'
		ORDER BY connected_at DESC
		LIMIT 1`,
		agentUserID, s.config.Database.EncryptionKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return token, nil
}
