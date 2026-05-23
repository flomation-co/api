package persistence

import (
	"database/sql"
	"time"

	"flomation.app/automate/api"
)

// UpsertGoogleAccount stores or updates a Google account connection
// for an agent_user with a specific purpose. The refresh_token is
// encrypted with PGP. The unique constraint is on
// (agent_user_id, google_email, purpose).
func (s *Service) UpsertGoogleAccount(agentUserID, email, refreshToken, label, purpose string) error {
	if purpose == "" {
		purpose = "calendar"
	}
	_, err := s.conn.Exec(`
		INSERT INTO agent_user_google_account (agent_user_id, google_email, refresh_token, label, purpose)
		VALUES ($1, $2, PGP_SYM_ENCRYPT($3, $4), $5, $6)
		ON CONFLICT (agent_user_id, google_email, purpose)
		DO UPDATE SET refresh_token = PGP_SYM_ENCRYPT($3, $4), label = $5, connected_at = NOW()
	`, agentUserID, email, refreshToken, s.config.Database.EncryptionKey, label, purpose)
	return err
}

// GetGoogleAccounts returns all connected Google accounts for an
// agent_user with decrypted refresh tokens. When purpose is non-empty,
// filters to only that purpose. Used by the internal token endpoint
// to exchange refresh tokens for access tokens.
func (s *Service) GetGoogleAccounts(agentUserID string, purpose ...string) ([]*api.AgentUserGoogleAccount, error) {
	var results []*api.AgentUserGoogleAccount
	var err error

	if len(purpose) > 0 && purpose[0] != "" {
		err = s.conn.Select(&results, `
			SELECT id, agent_user_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, status, last_error,
				token_expires_at, connected_at
			FROM agent_user_google_account
			WHERE agent_user_id = $1 AND purpose = $3
			ORDER BY connected_at ASC
		`, agentUserID, s.config.Database.EncryptionKey, purpose[0])
	} else {
		err = s.conn.Select(&results, `
			SELECT id, agent_user_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, status, last_error,
				token_expires_at, connected_at
			FROM agent_user_google_account
			WHERE agent_user_id = $1
			ORDER BY connected_at ASC
		`, agentUserID, s.config.Database.EncryptionKey)
	}

	if err != nil {
		return nil, err
	}
	return results, nil
}

// DeleteGoogleAccount removes a specific Google account connection.
// When purpose is provided, only deletes that specific purpose row.
// When purpose is empty, deletes ALL rows for that email (all purposes).
func (s *Service) DeleteGoogleAccount(agentUserID, email string, purpose ...string) error {
	var result sql.Result
	var err error

	if len(purpose) > 0 && purpose[0] != "" {
		result, err = s.conn.Exec(`
			DELETE FROM agent_user_google_account
			WHERE agent_user_id = $1 AND google_email = $2 AND purpose = $3
		`, agentUserID, email, purpose[0])
	} else {
		result, err = s.conn.Exec(`
			DELETE FROM agent_user_google_account
			WHERE agent_user_id = $1 AND google_email = $2
		`, agentUserID, email)
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

// GoogleAccountRefreshRow contains decrypted data needed to refresh a Google account token.
type GoogleAccountRefreshRow struct {
	ID           string `db:"id"`
	RefreshToken string `db:"refresh_token"`
	Purpose      string `db:"purpose"`
	Email        string `db:"google_email"`
}

// GetGoogleAccountsNeedingRefresh returns accounts whose cached access token
// is missing or expiring within the given window.
func (s *Service) GetGoogleAccountsNeedingRefresh(within time.Duration) ([]GoogleAccountRefreshRow, error) {
	var rows []GoogleAccountRefreshRow
	if err := s.conn.Select(&rows, `
		SELECT id, PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
		       purpose, google_email
		FROM agent_user_google_account
		WHERE status = 'active'
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

// StoreGoogleAccountAccessToken caches a fresh access token with its expiry.
func (s *Service) StoreGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error {
	_, err := s.conn.Exec(`
		UPDATE agent_user_google_account
		SET access_token = PGP_SYM_ENCRYPT($2, $3),
		    token_expires_at = $4,
		    status = 'active',
		    last_error = NULL
		WHERE id = $1`,
		id, accessToken, s.config.Database.EncryptionKey, expiresAt)
	return err
}

// UpdateGoogleAccountStatus sets the status and optional error message.
func (s *Service) UpdateGoogleAccountStatus(id, status string, lastError *string) error {
	_, err := s.conn.Exec(`
		UPDATE agent_user_google_account
		SET status = $2, last_error = $3
		WHERE id = $1`, id, status, lastError)
	return err
}

// GetGoogleAccountAccessToken returns the cached decrypted access token
// for a specific account, or empty string if not cached or expired.
func (s *Service) GetGoogleAccountAccessToken(id string) (string, error) {
	var token string
	err := s.conn.Get(&token, `
		SELECT COALESCE(PGP_SYM_DECRYPT(access_token, $2), '')
		FROM agent_user_google_account
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
