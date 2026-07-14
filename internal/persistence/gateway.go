package persistence

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/utils"
)

// gatewayAPIIDLength is the length of the short, url-safe gateway api id — the
// public /gw/<api_id> token. Base-52 (a-zA-Z), so 12 chars ≈ 52^12 of entropy:
// unguessable, yet far shorter than the 36-char UUID it replaces in the URL.
const gatewayAPIIDLength = 12

// gatewayAPIIDMaxAttempts bounds the mint-and-retry loop on the (rare) api_id
// unique collision.
const gatewayAPIIDMaxAttempts = 6

// GenerateGatewayAPIID mints a fresh short api id.
func GenerateGatewayAPIID() string {
	return utils.GenerateRandomStringID(gatewayAPIIDLength)
}

// gatewayScope returns a WHERE predicate (placeholders from `start`) + args,
// scoping a gateway api to an organisation, or to a personal owner when nil.
// Mirrors embedScope so ownership semantics stay identical across the SDK
// surfaces.
func gatewayScope(ownerID string, orgID *string, start int) (string, []interface{}) {
	if orgID != nil {
		return fmt.Sprintf("organisation_id = $%d", start), []interface{}{*orgID}
	}
	return fmt.Sprintf("owner_id = $%d AND organisation_id IS NULL", start), []interface{}{ownerID}
}

func gatewayAuthConfig(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// CreateGatewayAPI inserts a new gateway api, minting a unique short api_id
// (retrying on the rare collision). auth_type/auth_config/secret come from the
// supplied struct; secret material must already be hashed by the caller.
func (s *Service) CreateGatewayAPI(a *api.GatewayAPI) (*api.GatewayAPI, error) {
	authType := a.AuthType
	if authType == "" {
		authType = "open"
	}
	var lastErr error
	for attempt := 0; attempt < gatewayAPIIDMaxAttempts; attempt++ {
		apiID := GenerateGatewayAPIID()
		err := s.conn.QueryRow(`
			INSERT INTO gateway_api
				(api_id, organisation_id, owner_id, name, auth_type, auth_config, auth_secret_hash, auth_secret_salt)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
			RETURNING id, api_id, created_at, updated_at`,
			apiID, a.OrganisationID, a.OwnerID, a.Name, authType,
			gatewayAuthConfig(a.AuthConfig), a.AuthSecretHash, a.AuthSecretSalt,
		).Scan(&a.ID, &a.APIID, &a.CreatedAt, &a.UpdatedAt)
		if err == nil {
			a.AuthType = authType
			if len(a.AuthConfig) == 0 {
				a.AuthConfig = json.RawMessage("{}")
			}
			return a, nil
		}
		// Retry only on the api_id unique collision; anything else is fatal.
		if strings.Contains(err.Error(), "gateway_api_api_id_key") || strings.Contains(err.Error(), "duplicate key") {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("could not mint a unique gateway api id after %d attempts: %w", gatewayAPIIDMaxAttempts, lastErr)
}

// ListGatewayAPIs returns every gateway api in scope, each with its endpoints.
func (s *Service) ListGatewayAPIs(ownerID string, orgID *string) ([]api.GatewayAPI, error) {
	pred, args := gatewayScope(ownerID, orgID, 1)
	var apis []api.GatewayAPI
	if err := s.conn.Select(&apis, `
		SELECT id, api_id, organisation_id, owner_id, name, auth_type, auth_config,
		       auth_secret_hash, auth_secret_salt, created_at, updated_at
		FROM gateway_api WHERE `+pred+` ORDER BY created_at DESC`, args...); err != nil {
		return nil, err
	}
	for i := range apis {
		eps, err := s.loadGatewayEndpoints(apis[i].ID)
		if err != nil {
			return nil, err
		}
		apis[i].Endpoints = eps
	}
	return apis, nil
}

// GetGatewayAPI returns a single gateway api in scope (with endpoints), or
// (nil, nil) when none matches — the ownership check before any mutation.
func (s *Service) GetGatewayAPI(id, ownerID string, orgID *string) (*api.GatewayAPI, error) {
	pred, scopeArgs := gatewayScope(ownerID, orgID, 2)
	args := append([]interface{}{id}, scopeArgs...)
	var a api.GatewayAPI
	err := s.conn.Get(&a, `
		SELECT id, api_id, organisation_id, owner_id, name, auth_type, auth_config,
		       auth_secret_hash, auth_secret_salt, created_at, updated_at
		FROM gateway_api WHERE id = $1 AND `+pred, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	eps, err := s.loadGatewayEndpoints(a.ID)
	if err != nil {
		return nil, err
	}
	a.Endpoints = eps
	return &a, nil
}

// UpdateGatewayAPI updates the name + auth policy of a gateway api in scope.
// A nil authSecretHash/Salt leaves the stored secret untouched (so a plain rename
// or an auth-config edit doesn't wipe an existing key); pass a fresh hash+salt to
// rotate, or empty strings to clear.
func (s *Service) UpdateGatewayAPI(a *api.GatewayAPI, ownerID string, orgID *string, secretHash, secretSalt *string) (bool, error) {
	// Params: $1 id, $2 name, $3 auth_type, $4 auth_config, $5 set-secret flag,
	// $6 hash, $7 salt, then the scope predicate starts at $8.
	pred, scopeArgs := gatewayScope(ownerID, orgID, 8)
	var hashArg, saltArg interface{}
	if secretHash != nil {
		hashArg, saltArg = *secretHash, *secretSalt
	}
	args := []interface{}{a.ID, a.Name, a.AuthType, gatewayAuthConfig(a.AuthConfig), secretHash != nil, hashArg, saltArg}
	args = append(args, scopeArgs...)
	res, err := s.conn.Exec(`
		UPDATE gateway_api
		SET name = $2,
		    auth_type = $3,
		    auth_config = $4::jsonb,
		    auth_secret_hash = CASE WHEN $5 THEN $6 ELSE auth_secret_hash END,
		    auth_secret_salt = CASE WHEN $5 THEN $7 ELSE auth_secret_salt END,
		    updated_at = NOW()
		WHERE id = $1 AND `+pred, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteGatewayAPI removes a gateway api (cascading its endpoints) in scope.
func (s *Service) DeleteGatewayAPI(id, ownerID string, orgID *string) (bool, error) {
	pred, scopeArgs := gatewayScope(ownerID, orgID, 2)
	args := append([]interface{}{id}, scopeArgs...)
	res, err := s.conn.Exec(`DELETE FROM gateway_api WHERE id = $1 AND `+pred, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Service) loadGatewayEndpoints(apiID string) ([]api.GatewayEndpoint, error) {
	var eps []api.GatewayEndpoint
	if err := s.conn.Select(&eps, `
		SELECT id, gateway_api_id, method, path_pattern, flow_id, trigger_id, enabled, created_at
		FROM gateway_endpoint WHERE gateway_api_id = $1 ORDER BY path_pattern, method`, apiID); err != nil {
		return nil, err
	}
	return eps, nil
}

// CreateGatewayEndpoint inserts an endpoint under a gateway api. The caller must
// already have verified the api is in the caller's scope.
func (s *Service) CreateGatewayEndpoint(e *api.GatewayEndpoint) (*api.GatewayEndpoint, error) {
	if err := s.conn.QueryRow(`
		INSERT INTO gateway_endpoint (gateway_api_id, method, path_pattern, flow_id, trigger_id, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		e.GatewayAPIID, strings.ToUpper(e.Method), e.PathPattern, e.FlowID, e.TriggerID, e.Enabled,
	).Scan(&e.ID, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Method = strings.ToUpper(e.Method)
	return e, nil
}

// UpdateGatewayEndpoint updates an endpoint, scoped to its gateway api (so a
// caller can only touch endpoints of an api they own — the handler passes the
// verified api id).
func (s *Service) UpdateGatewayEndpoint(e *api.GatewayEndpoint) (bool, error) {
	res, err := s.conn.Exec(`
		UPDATE gateway_endpoint
		SET method = $3, path_pattern = $4, flow_id = $5, trigger_id = $6, enabled = $7
		WHERE id = $1 AND gateway_api_id = $2`,
		e.ID, e.GatewayAPIID, strings.ToUpper(e.Method), e.PathPattern, e.FlowID, e.TriggerID, e.Enabled)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteGatewayEndpoint removes an endpoint under a specific gateway api.
func (s *Service) DeleteGatewayEndpoint(id, gatewayAPIID string) (bool, error) {
	res, err := s.conn.Exec(`DELETE FROM gateway_endpoint WHERE id = $1 AND gateway_api_id = $2`, id, gatewayAPIID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResolveGatewayAPI returns everything the Launch edge needs to serve
// /gw/<api_id>/*: the auth policy (incl. the salted-hash material for
// api_key/basic compares) and the enabled endpoints. Public lookup by api_id —
// no owner scope (the api_id IS the capability). Returns (nil, nil) when unknown.
func (s *Service) ResolveGatewayAPI(apiID string) (*api.GatewayResolution, error) {
	var a api.GatewayAPI
	err := s.conn.Get(&a, `
		SELECT id, api_id, organisation_id, owner_id, name, auth_type, auth_config,
		       auth_secret_hash, auth_secret_salt, created_at, updated_at
		FROM gateway_api WHERE api_id = $1`, apiID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	eps, err := s.loadGatewayEndpoints(a.ID)
	if err != nil {
		return nil, err
	}
	enabled := make([]api.GatewayEndpoint, 0, len(eps))
	for _, e := range eps {
		if e.Enabled {
			enabled = append(enabled, e)
		}
	}
	res := &api.GatewayResolution{
		APIID:          a.APIID,
		OrganisationID: a.OrganisationID,
		OwnerID:        a.OwnerID,
		AuthType:       a.AuthType,
		AuthConfig:     a.AuthConfig,
		Endpoints:      enabled,
	}
	if a.AuthSecretHash != nil {
		res.AuthSecretHash = *a.AuthSecretHash
	}
	if a.AuthSecretSalt != nil {
		res.AuthSecretSalt = *a.AuthSecretSalt
	}
	return res, nil
}
