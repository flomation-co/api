package persistence

import (
	"database/sql"

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
				scopes, label, purpose, connected_at
			FROM agent_user_google_account
			WHERE agent_user_id = $1 AND purpose = $3
			ORDER BY connected_at ASC
		`, agentUserID, s.config.Database.EncryptionKey, purpose[0])
	} else {
		err = s.conn.Select(&results, `
			SELECT id, agent_user_id, google_email,
				PGP_SYM_DECRYPT(refresh_token, $2) AS refresh_token,
				scopes, label, purpose, connected_at
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
