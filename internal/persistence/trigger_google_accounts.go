package persistence

import (
	"database/sql"
	"time"

	"flomation.app/automate/api"
)

// UpsertTriggerGoogleAccount stores or updates a Google account connection
// scoped to a trigger. The refresh_token is encrypted with PGP.
//
// Mirrors UpsertGoogleAccount: re-linking resets status/last_error
// and clears the stale cached access_token so the next refresh
// attempt starts from a clean slate.
func (s *Service) UpsertTriggerGoogleAccount(triggerID, email, refreshToken, label, purpose string) error {
	if purpose == "" {
		purpose = "email_read"
	}
	_, err := s.conn.Exec(`
		INSERT INTO trigger_google_account (trigger_id, google_email, refresh_token, label, purpose)
		VALUES ($1, $2, PGP_SYM_ENCRYPT($3, $4), $5, $6)
		ON CONFLICT (trigger_id, google_email, purpose)
		DO UPDATE SET refresh_token = PGP_SYM_ENCRYPT($3, $4),
		              label = $5,
		              connected_at = NOW(),
		              status = 'active',
		              last_error = NULL,
		              access_token = NULL,
		              token_expires_at = NULL,
		              consecutive_failures = 0
	`, triggerID, email, refreshToken, s.config.Database.EncryptionKey, label, purpose)
	return err
}

// GetTriggerGoogleAccounts returns all connected Google accounts for a
// trigger with decrypted refresh tokens. Optionally filter by purpose.
func (s *Service) GetTriggerGoogleAccounts(triggerID string, purpose ...string) ([]*api.TriggerGoogleAccount, error) {
	var results []*api.TriggerGoogleAccount
	var err error

	if len(purpose) > 0 && purpose[0] != "" {
		err = s.conn.Select(&results, `
			SELECT id, trigger_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, status, last_error,
				token_expires_at, connected_at
			FROM trigger_google_account
			WHERE trigger_id = $1 AND purpose = $3
			ORDER BY connected_at ASC
		`, triggerID, s.config.Database.EncryptionKey, purpose[0])
	} else {
		err = s.conn.Select(&results, `
			SELECT id, trigger_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, status, last_error,
				token_expires_at, connected_at
			FROM trigger_google_account
			WHERE trigger_id = $1
			ORDER BY connected_at ASC
		`, triggerID, s.config.Database.EncryptionKey)
	}

	if err != nil {
		return nil, err
	}
	return results, nil
}

// DeleteTriggerGoogleAccount removes a specific account from a trigger.
func (s *Service) DeleteTriggerGoogleAccount(triggerID, email string, purpose ...string) error {
	var result sql.Result
	var err error

	if len(purpose) > 0 && purpose[0] != "" {
		result, err = s.conn.Exec(`
			DELETE FROM trigger_google_account
			WHERE trigger_id = $1 AND google_email = $2 AND purpose = $3
		`, triggerID, email, purpose[0])
	} else {
		result, err = s.conn.Exec(`
			DELETE FROM trigger_google_account
			WHERE trigger_id = $1 AND google_email = $2
		`, triggerID, email)
	}

	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetTriggerGoogleAccountsNeedingRefresh returns trigger-scoped accounts
// whose cached access token is missing or expiring within the given window.
//
// Includes status='error' rows for the same reason as the agent-user
// variant — see GetGoogleAccountsNeedingRefresh.
func (s *Service) GetTriggerGoogleAccountsNeedingRefresh(within time.Duration) ([]GoogleAccountRefreshRow, error) {
	var rows []GoogleAccountRefreshRow
	if err := s.conn.Select(&rows, `
		SELECT id, PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
		       purpose, google_email
		FROM trigger_google_account
		WHERE status IN ('active', 'error')
		  AND refresh_token IS NOT NULL
		  AND (
		      access_token IS NULL
		      OR token_expires_at IS NULL
		      OR token_expires_at < NOW() + $1::INTERVAL
		  )`,
		within.String(), s.config.Database.EncryptionKey); err != nil {
		return nil, err
	}
	return rows, nil
}

// StoreTriggerGoogleAccountAccessToken caches a fresh access token.
// See StoreGoogleAccountAccessToken for the rationale on clearing the
// consecutive-failures counter.
func (s *Service) StoreTriggerGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error {
	_, err := s.conn.Exec(`
		UPDATE trigger_google_account
		SET access_token = PGP_SYM_ENCRYPT($2, $3),
		    token_expires_at = $4,
		    status = 'active',
		    last_error = NULL,
		    consecutive_failures = 0
		WHERE id = $1`,
		id, accessToken, s.config.Database.EncryptionKey, expiresAt)
	return err
}

// UpdateTriggerGoogleAccountStatus sets the status and optional error
// message. See UpdateGoogleAccountStatus for the policy split between
// this method and RecordTriggerGoogleAccountRefreshFailure.
func (s *Service) UpdateTriggerGoogleAccountStatus(id, status string, lastError *string) error {
	_, err := s.conn.Exec(`
		UPDATE trigger_google_account
		SET status = $2, last_error = $3
		WHERE id = $1`, id, status, lastError)
	return err
}

// RecordTriggerGoogleAccountRefreshFailure mirrors
// RecordGoogleAccountRefreshFailure for the trigger-scoped table.
func (s *Service) RecordTriggerGoogleAccountRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error) {
	var newStatus string
	err := s.conn.Get(&newStatus, `
		UPDATE trigger_google_account
		SET consecutive_failures = consecutive_failures + 1,
		    last_error = $2,
		    status = CASE
		        WHEN $3::BOOLEAN OR consecutive_failures + 1 >= $4 THEN 'revoked'
		        ELSE 'error'
		    END
		WHERE id = $1
		RETURNING status`, id, lastError, permanent, threshold)
	return newStatus, err
}

// GetTriggerGoogleAccountAccessToken returns the cached decrypted access token.
func (s *Service) GetTriggerGoogleAccountAccessToken(id string) (string, error) {
	var token string
	err := s.conn.Get(&token, `
		SELECT COALESCE(PGP_SYM_DECRYPT(access_token, $2), '')
		FROM trigger_google_account
		WHERE id = $1
		  AND access_token IS NOT NULL
		  AND token_expires_at > NOW()
		  AND status = 'active'`,
		id, s.config.Database.EncryptionKey)
	if err != nil {
		return "", err
	}
	return token, nil
}
