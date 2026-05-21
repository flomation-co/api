package persistence

import (
	"database/sql"
	"time"

	api "flomation.app/automate/api"
)

// ── Credential providers ────────────────────────────────────────────

// GetCredentialProviders returns all configured OAuth providers.
func (s *Service) GetCredentialProviders() ([]api.CredentialProvider, error) {
	var providers []api.CredentialProvider
	if err := s.conn.Select(&providers, `SELECT * FROM credential_provider ORDER BY name`); err != nil {
		return nil, err
	}
	return providers, nil
}

// GetCredentialProvider returns a single provider by slug.
func (s *Service) GetCredentialProvider(slug string) (*api.CredentialProvider, error) {
	var provider api.CredentialProvider
	if err := s.conn.Get(&provider, `SELECT * FROM credential_provider WHERE slug = $1`, slug); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &provider, nil
}

// ── Environment credentials ─────────────────────────────────────────

// GetCredentialsByEnvironmentID returns all credentials for an environment (without decrypted tokens).
func (s *Service) GetCredentialsByEnvironmentID(environmentID string) ([]api.EnvironmentCredential, error) {
	var creds []api.EnvironmentCredential
	if err := s.conn.Select(&creds, `
		SELECT ec.id, ec.environment_id, ec.provider_slug, ec.name,
		       ec.token_expires_at, ec.scopes, ec.status,
		       ec.last_refreshed_at, ec.last_error, ec.metadata,
		       ec.created_at, ec.updated_at,
		       cp.name AS provider_name, cp.icon AS provider_icon
		FROM environment_credential ec
		JOIN credential_provider cp ON cp.slug = ec.provider_slug
		WHERE ec.environment_id = $1
		ORDER BY ec.name`, environmentID); err != nil {
		return nil, err
	}
	return creds, nil
}

// GetCredentialByID returns a credential by ID (without decrypted tokens).
func (s *Service) GetCredentialByID(id string) (*api.EnvironmentCredential, error) {
	var cred api.EnvironmentCredential
	if err := s.conn.Get(&cred, `
		SELECT ec.*, cp.name AS provider_name, cp.icon AS provider_icon
		FROM environment_credential ec
		JOIN credential_provider cp ON cp.slug = ec.provider_slug
		WHERE ec.id = $1`, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &cred, nil
}

// GetCredentialAccessToken returns the decrypted access token for a credential.
func (s *Service) GetCredentialAccessToken(credentialID, environmentKey string) (*string, error) {
	var token *string
	if err := s.conn.Get(&token, `
		SELECT PGP_SYM_DECRYPT(access_token, $2) AS access_token
		FROM environment_credential
		WHERE id = $1 AND access_token IS NOT NULL`, credentialID, environmentKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return token, nil
}

// GetCredentialByName returns the decrypted access token for a named credential in an environment.
func (s *Service) GetCredentialByName(environmentID, name, environmentKey string) (*string, error) {
	var token *string
	if err := s.conn.Get(&token, `
		SELECT PGP_SYM_DECRYPT(access_token, $3) AS access_token
		FROM environment_credential
		WHERE environment_id = $1 AND name = $2 AND status = 'active'
		AND access_token IS NOT NULL`, environmentID, name, environmentKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return token, nil
}

// CreateCredential creates a new credential record in pending state.
func (s *Service) CreateCredential(cred *api.EnvironmentCredential, environmentKey string) (string, error) {
	var id string
	err := s.conn.QueryRow(`
		INSERT INTO environment_credential (
			environment_id, provider_slug, name, scopes, status,
			client_id, client_secret
		) VALUES (
			$1, $2, $3, $4, $5,
			CASE WHEN $6 = '' THEN NULL ELSE PGP_SYM_ENCRYPT($6, $7) END,
			CASE WHEN $8 = '' THEN NULL ELSE PGP_SYM_ENCRYPT($8, $7) END
		) RETURNING id`,
		cred.EnvironmentID, cred.ProviderSlug, cred.Name, cred.Scopes, api.CredentialStatusPending,
		stringOrEmpty(cred.ClientID), environmentKey,
		stringOrEmpty(cred.ClientSecret),
	).Scan(&id)
	return id, err
}

// StoreCredentialTokens saves the OAuth tokens after a successful authorization.
func (s *Service) StoreCredentialTokens(id, environmentKey, accessToken, refreshToken string, expiresAt *time.Time) error {
	_, err := s.conn.Exec(`
		UPDATE environment_credential
		SET access_token = PGP_SYM_ENCRYPT($2, $3),
		    refresh_token = CASE WHEN $4 = '' THEN refresh_token ELSE PGP_SYM_ENCRYPT($4, $3) END,
		    token_expires_at = $5,
		    status = 'active',
		    last_refreshed_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1`,
		id, accessToken, environmentKey, refreshToken, expiresAt)
	return err
}

// GetCredentialsNeedingRefresh returns credentials expiring within the given window.
// Tokens are decrypted using the environment's secret_key (itself decrypted with the global encryption key).
func (s *Service) GetCredentialsNeedingRefresh(within time.Duration) ([]CredentialRefreshRow, error) {
	var rows []CredentialRefreshRow
	if err := s.conn.Select(&rows, `
		SELECT ec.id, ec.environment_id, ec.provider_slug,
		       PGP_SYM_DECRYPT(ec.refresh_token, PGP_SYM_DECRYPT(e.secret_key, $2)) AS refresh_token,
		       PGP_SYM_DECRYPT(ec.client_id, PGP_SYM_DECRYPT(e.secret_key, $2)) AS client_id,
		       PGP_SYM_DECRYPT(ec.client_secret, PGP_SYM_DECRYPT(e.secret_key, $2)) AS client_secret,
		       cp.token_url,
		       PGP_SYM_DECRYPT(e.secret_key, $2) AS environment_key
		FROM environment_credential ec
		JOIN environment e ON e.id = ec.environment_id
		JOIN credential_provider cp ON cp.slug = ec.provider_slug
		WHERE ec.status = 'active'
		  AND ec.refresh_token IS NOT NULL
		  AND ec.token_expires_at IS NOT NULL
		  AND ec.token_expires_at < NOW() + $1::INTERVAL`,
		within.String(), s.config.Database.EncryptionKey); err != nil {
		return nil, err
	}
	return rows, nil
}

// CredentialRefreshRow contains the decrypted data needed to refresh a credential.
type CredentialRefreshRow struct {
	ID             string  `db:"id"`
	EnvironmentID  string  `db:"environment_id"`
	ProviderSlug   string  `db:"provider_slug"`
	RefreshToken   *string `db:"refresh_token"`
	ClientID       *string `db:"client_id"`
	ClientSecret   *string `db:"client_secret"`
	TokenURL       string  `db:"token_url"`
	EnvironmentKey string  `db:"environment_key"`
}

// UpdateCredentialStatus sets the status and optional error message.
func (s *Service) UpdateCredentialStatus(id, status string, lastError *string) error {
	_, err := s.conn.Exec(`
		UPDATE environment_credential
		SET status = $2, last_error = $3, updated_at = NOW()
		WHERE id = $1`, id, status, lastError)
	return err
}

// DeleteCredential removes a credential.
func (s *Service) DeleteCredential(id, environmentID string) error {
	_, err := s.conn.Exec(`
		DELETE FROM environment_credential
		WHERE id = $1 AND environment_id = $2`, id, environmentID)
	return err
}

// GetDecryptedClientCredentials returns the decrypted client_id and client_secret.
func (s *Service) GetDecryptedClientCredentials(credentialID, environmentKey string) (clientID, clientSecret *string, err error) {
	var row struct {
		ClientID     *string `db:"client_id"`
		ClientSecret *string `db:"client_secret"`
	}
	err = s.conn.Get(&row, `
		SELECT
			PGP_SYM_DECRYPT(client_id, $2) AS client_id,
			PGP_SYM_DECRYPT(client_secret, $2) AS client_secret
		FROM environment_credential
		WHERE id = $1`, credentialID, environmentKey)
	if err != nil {
		return nil, nil, err
	}
	return row.ClientID, row.ClientSecret, nil
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
