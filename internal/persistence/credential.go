package persistence

import (
	"database/sql"
	"encoding/json"
	"time"

	api "flomation.app/automate/api"
)

// ── Credential providers ────────────────────────────────────────────

// GetCredentialProviders returns all configured OAuth providers.
// credentialProviderColumns is an explicit column list (rather than SELECT *)
// so an additive column like url_variables is decoupled from the struct — a
// binary rollback to a build without the new field can't break provider scans.
// #nosec G101 -- this is a SQL column list, not a credential (the const name trips gosec's identifier heuristic).
const credentialProviderColumns = `slug, name, icon, auth_url, token_url, revoke_url, default_scopes, url_variables, created_at`

func (s *Service) GetCredentialProviders() ([]api.CredentialProvider, error) {
	var providers []api.CredentialProvider
	if err := s.conn.Select(&providers, `SELECT `+credentialProviderColumns+` FROM credential_provider ORDER BY name`); err != nil {
		return nil, err
	}
	return providers, nil
}

// GetCredentialProvider returns a single provider by slug.
func (s *Service) GetCredentialProvider(slug string) (*api.CredentialProvider, error) {
	var provider api.CredentialProvider
	if err := s.conn.Get(&provider, `SELECT `+credentialProviderColumns+` FROM credential_provider WHERE slug = $1`, slug); err != nil {
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
	var metadata interface{}
	if cred.Metadata != nil {
		metadata = []byte(*cred.Metadata)
	}
	var id string
	err := s.conn.QueryRow(`
		INSERT INTO environment_credential (
			environment_id, provider_slug, name, scopes, status,
			client_id, client_secret, metadata
		) VALUES (
			$1, $2, $3, $4, $5,
			CASE WHEN $6 = '' THEN NULL ELSE PGP_SYM_ENCRYPT($6, $7) END,
			CASE WHEN $8 = '' THEN NULL ELSE PGP_SYM_ENCRYPT($8, $7) END,
			$9
		) RETURNING id`,
		cred.EnvironmentID, cred.ProviderSlug, cred.Name, cred.Scopes, api.CredentialStatusPending,
		stringOrEmpty(cred.ClientID), environmentKey,
		stringOrEmpty(cred.ClientSecret), metadata,
	).Scan(&id)
	return id, err
}

// CreateAWSRoleCredential creates an immediately-active AWS Role credential. The
// per-credential Flomation IAM user's SECRET access key is encrypted into
// access_token (with the environment key, like any credential secret); its
// non-secret fields (role_arn, external_id, region, iam_user_arn,
// base_access_key_id) live in the plaintext metadata JSONB. baseSecret may be ""
// for the single-principal fallback, in which case no token is stored.
func (s *Service) CreateAWSRoleCredential(environmentID, name, environmentKey, baseSecret string, metadata json.RawMessage) (string, error) {
	var id string
	err := s.conn.QueryRow(`
		INSERT INTO environment_credential (
			environment_id, provider_slug, name, status, access_token, metadata
		) VALUES (
			$1, 'aws_role', $2, 'active',
			CASE WHEN $3 = '' THEN NULL ELSE PGP_SYM_ENCRYPT($3, $4) END,
			$5
		)
		RETURNING id`,
		environmentID, name, baseSecret, environmentKey, []byte(metadata),
	).Scan(&id)
	return id, err
}

// StoreCredentialTokens saves the OAuth tokens after a successful authorization.
// clientID/clientSecret are persisted so the background refresh poller can use them
// without needing access to application config (supports config-default credentials).
func (s *Service) StoreCredentialTokens(id, environmentKey, accessToken, refreshToken, clientID, clientSecret string, expiresAt *time.Time) error {
	_, err := s.conn.Exec(`
		UPDATE environment_credential
		SET access_token = PGP_SYM_ENCRYPT($2, $3),
		    refresh_token = CASE WHEN $4 = '' THEN refresh_token ELSE PGP_SYM_ENCRYPT($4, $3) END,
		    token_expires_at = $5,
		    client_id = CASE WHEN $6 = '' THEN client_id ELSE PGP_SYM_ENCRYPT($6, $3) END,
		    client_secret = CASE WHEN $7 = '' THEN client_secret ELSE PGP_SYM_ENCRYPT($7, $3) END,
		    status = 'active',
		    last_refreshed_at = NOW(),
		    last_error = NULL,
		    consecutive_failures = 0,
		    updated_at = NOW()
		WHERE id = $1`,
		id, accessToken, environmentKey, refreshToken, expiresAt, clientID, clientSecret)
	return err
}

// GetCredentialWithMetaByName resolves an active credential's decrypted access
// token AND its (plaintext JSONB) metadata by name. The metadata carries the
// per-account identifier captured after OAuth — QuickBooks realm_id / Xero
// tenant_id — which the executor reads via ${credentials.<name>.<key>}. Returns
// (nil, nil, nil) when no active credential matches.
func (s *Service) GetCredentialWithMetaByName(environmentID, name, environmentKey string) (*string, *json.RawMessage, error) {
	var row struct {
		AccessToken *string          `db:"access_token"`
		Metadata    *json.RawMessage `db:"metadata"`
	}
	// No "access_token IS NOT NULL" guard: token-less credentials (e.g. aws_role,
	// which carry only metadata — role_arn/external_id/region) must still resolve.
	// PGP_SYM_DECRYPT(NULL, key) returns NULL, so AccessToken scans as nil and
	// only the metadata is served.
	if err := s.conn.Get(&row, `
		SELECT PGP_SYM_DECRYPT(access_token, $3) AS access_token, metadata
		FROM environment_credential
		WHERE environment_id = $1 AND name = $2 AND status = 'active'`, environmentID, name, environmentKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return row.AccessToken, row.Metadata, nil
}

// GetCredentialWithMetaByID resolves a credential's decrypted access token and
// metadata by id (used to test AWS Role access with the credential's own base
// keys). Returns (nil, nil, nil) when no active credential matches.
func (s *Service) GetCredentialWithMetaByID(id, environmentKey string) (*string, *json.RawMessage, error) {
	var row struct {
		AccessToken *string          `db:"access_token"`
		Metadata    *json.RawMessage `db:"metadata"`
	}
	if err := s.conn.Get(&row, `
		SELECT PGP_SYM_DECRYPT(access_token, $2) AS access_token, metadata
		FROM environment_credential
		WHERE id = $1 AND status = 'active'`, id, environmentKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return row.AccessToken, row.Metadata, nil
}

// IncompleteAWSRoleCredential identifies an aws_role credential whose wizard was
// never completed (no role_arn attached), for cleanup.
type IncompleteAWSRoleCredential struct {
	ID            string `db:"id"`
	EnvironmentID string `db:"environment_id"`
	IAMUserName   string `db:"iam_user_name"`
}

// ListIncompleteAWSRoleCredentials returns aws_role credentials older than
// olderThanSeconds that still have no role_arn in metadata — i.e. wizards
// abandoned after minting the identity but before attaching a role.
func (s *Service) ListIncompleteAWSRoleCredentials(olderThanSeconds int) ([]IncompleteAWSRoleCredential, error) {
	var rows []IncompleteAWSRoleCredential
	err := s.conn.Select(&rows, `
		SELECT id, environment_id, COALESCE(metadata->>'iam_user_name', '') AS iam_user_name
		FROM environment_credential
		WHERE provider_slug = 'aws_role'
		  AND created_at < NOW() - make_interval(secs => $1)
		  AND COALESCE(metadata->>'role_arn', '') = ''`, olderThanSeconds)
	return rows, err
}

// UpdateCredentialMetadata overwrites a credential's metadata JSONB column.
// Used by the OAuth callback to persist the per-account identifier discovered
// only after authorisation (QuickBooks realmId returned on the callback; Xero
// tenantId fetched from /connections) — a post-auth value, unlike a url_var
// which the user supplies up front. Passing nil clears the column.
func (s *Service) UpdateCredentialMetadata(id string, metadata *json.RawMessage) error {
	var m interface{}
	if metadata != nil {
		m = []byte(*metadata)
	}
	_, err := s.conn.Exec(
		`UPDATE environment_credential SET metadata = $2, updated_at = NOW() WHERE id = $1`,
		id, m)
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
		       ec.metadata,
		       PGP_SYM_DECRYPT(e.secret_key, $2) AS environment_key
		FROM environment_credential ec
		JOIN environment e ON e.id = ec.environment_id
		JOIN credential_provider cp ON cp.slug = ec.provider_slug
		WHERE ec.status IN ('active', 'error')
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
	ID             string           `db:"id"`
	EnvironmentID  string           `db:"environment_id"`
	ProviderSlug   string           `db:"provider_slug"`
	RefreshToken   *string          `db:"refresh_token"`
	ClientID       *string          `db:"client_id"`
	ClientSecret   *string          `db:"client_secret"`
	TokenURL       string           `db:"token_url"`
	Metadata       *json.RawMessage `db:"metadata"`
	EnvironmentKey string           `db:"environment_key"`
}

// UpdateCredentialStatus sets the status and optional error message.
// Does not touch consecutive_failures — the poller uses
// RecordCredentialRefreshFailure for atomic counter+status moves.
func (s *Service) UpdateCredentialStatus(id, status string, lastError *string) error {
	_, err := s.conn.Exec(`
		UPDATE environment_credential
		SET status = $2, last_error = $3, updated_at = NOW()
		WHERE id = $1`, id, status, lastError)
	return err
}

// RecordCredentialRefreshFailure mirrors the Google variant: bump
// the counter, decide whether to stay at 'error' (transient) or
// flip to 'revoked' (permanent error or threshold reached).
//
// Previously credentials transitioned to 'error' on the first
// failure and were excluded from refresh queries until the user
// re-OAuth'd manually — overly aggressive for transient failures
// (a brief OAuth-provider outage shouldn't permanently break a
// credential). The new behaviour retries up to `threshold` times
// for transient errors and only gives up when the provider tells
// us the refresh token is dead.
func (s *Service) RecordCredentialRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error) {
	var newStatus string
	err := s.conn.Get(&newStatus, `
		UPDATE environment_credential
		SET consecutive_failures = consecutive_failures + 1,
		    last_error = $2,
		    status = CASE
		        WHEN $3::BOOLEAN OR consecutive_failures + 1 >= $4 THEN 'revoked'
		        ELSE 'error'
		    END,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING status`, id, lastError, permanent, threshold)
	return newStatus, err
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
