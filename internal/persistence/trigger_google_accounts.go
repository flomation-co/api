package persistence

import (
	"database/sql"

	"flomation.app/automate/api"
)

// UpsertTriggerGoogleAccount stores or updates a Google account connection
// scoped to a trigger. The refresh_token is encrypted with PGP.
func (s *Service) UpsertTriggerGoogleAccount(triggerID, email, refreshToken, label, purpose string) error {
	if purpose == "" {
		purpose = "email_read"
	}
	_, err := s.conn.Exec(`
		INSERT INTO trigger_google_account (trigger_id, google_email, refresh_token, label, purpose)
		VALUES ($1, $2, PGP_SYM_ENCRYPT($3, $4), $5, $6)
		ON CONFLICT (trigger_id, google_email, purpose)
		DO UPDATE SET refresh_token = PGP_SYM_ENCRYPT($3, $4), label = $5, connected_at = NOW()
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
				scopes, label, purpose, connected_at
			FROM trigger_google_account
			WHERE trigger_id = $1 AND purpose = $3
			ORDER BY connected_at ASC
		`, triggerID, s.config.Database.EncryptionKey, purpose[0])
	} else {
		err = s.conn.Select(&results, `
			SELECT id, trigger_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, connected_at
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
