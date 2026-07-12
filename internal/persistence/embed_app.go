package persistence

import (
	"database/sql"
	"errors"
	"fmt"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/utils"
)

// embedPublishableKeyBytes is the random portion length of a publishable key.
// Keys look like "pk_<44 alnum chars>" — long enough to be unguessable, since a
// publishable key gates access together with the origin allowlist.
const embedPublishableKeyBytes = 44

// GenerateEmbedPublishableKey mints a fresh, unguessable publishable key. It is
// safe to expose in client JS (security comes from the origin allowlist +
// per-resource opt-in + server-side re-validation), so only the random suffix
// carries entropy.
func GenerateEmbedPublishableKey() string {
	return "pk_" + utils.GenerateRandomStringID(embedPublishableKeyBytes)
}

// embedScope returns a WHERE predicate (using placeholders starting at `start`)
// and its args, scoping an embed app to an organisation (organisation_id) or,
// when orgID is nil, to a personal owner (owner_id with a NULL organisation).
func embedScope(ownerID string, orgID *string, start int) (string, []interface{}) {
	if orgID != nil {
		return fmt.Sprintf("organisation_id = $%d", start), []interface{}{*orgID}
	}
	return fmt.Sprintf("owner_id = $%d AND organisation_id IS NULL", start), []interface{}{ownerID}
}

// dedupeEmbedOrigins removes blanks and duplicates while preserving order.
func dedupeEmbedOrigins(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// CreateEmbedApp inserts a new embed app (minting its publishable key) plus its
// initial allowed origins, atomically. The returned app carries the generated
// publishable key — the only time it is handed back in full.
func (s *Service) CreateEmbedApp(app *api.EmbedApp, origins []string) (*api.EmbedApp, error) {
	tx, err := s.conn.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	app.PublishableKey = GenerateEmbedPublishableKey()
	if err := tx.QueryRow(`
		INSERT INTO embed_app (organisation_id, owner_id, name, publishable_key)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		app.OrganisationID, app.OwnerID, app.Name, app.PublishableKey,
	).Scan(&app.ID, &app.CreatedAt); err != nil {
		return nil, err
	}

	origins = dedupeEmbedOrigins(origins)
	for _, o := range origins {
		if _, err := tx.Exec(`
			INSERT INTO embed_allowed_origin (embed_app_id, origin)
			VALUES ($1, $2) ON CONFLICT DO NOTHING`, app.ID, o); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	app.AllowedOrigins = origins
	return app, nil
}

// ListEmbedApps returns every embed app in the given scope, each with its allowed
// origins and opted-in resources loaded.
func (s *Service) ListEmbedApps(ownerID string, orgID *string) ([]api.EmbedApp, error) {
	pred, args := embedScope(ownerID, orgID, 1)
	var apps []api.EmbedApp
	if err := s.conn.Select(&apps, `
		SELECT id, organisation_id, owner_id, name, publishable_key, secret_key_hash, created_at
		FROM embed_app WHERE `+pred+` ORDER BY created_at DESC`, args...); err != nil {
		return nil, err
	}
	for i := range apps {
		if err := s.loadEmbedRelations(&apps[i]); err != nil {
			return nil, err
		}
	}
	return apps, nil
}

// GetEmbedApp returns a single embed app in scope (with relations), or (nil, nil)
// when none matches — used as the ownership check before mutating it.
func (s *Service) GetEmbedApp(id, ownerID string, orgID *string) (*api.EmbedApp, error) {
	pred, scopeArgs := embedScope(ownerID, orgID, 2)
	args := append([]interface{}{id}, scopeArgs...)
	var app api.EmbedApp
	err := s.conn.Get(&app, `
		SELECT id, organisation_id, owner_id, name, publishable_key, secret_key_hash, created_at
		FROM embed_app WHERE id = $1 AND `+pred, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadEmbedRelations(&app); err != nil {
		return nil, err
	}
	return &app, nil
}

// DeleteEmbedApp removes an app (cascading its origins + resource opt-ins) within
// the given scope. Returns false when nothing matched.
func (s *Service) DeleteEmbedApp(id, ownerID string, orgID *string) (bool, error) {
	pred, scopeArgs := embedScope(ownerID, orgID, 2)
	args := append([]interface{}{id}, scopeArgs...)
	res, err := s.conn.Exec(`DELETE FROM embed_app WHERE id = $1 AND `+pred, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AddEmbedOrigin / RemoveEmbedOrigin manage an app's allowed-origins list. The
// caller is expected to have already verified ownership via GetEmbedApp.
func (s *Service) AddEmbedOrigin(appID, origin string) error {
	_, err := s.conn.Exec(`
		INSERT INTO embed_allowed_origin (embed_app_id, origin)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, appID, origin)
	return err
}

func (s *Service) RemoveEmbedOrigin(appID, origin string) error {
	_, err := s.conn.Exec(`
		DELETE FROM embed_allowed_origin WHERE embed_app_id = $1 AND origin = $2`, appID, origin)
	return err
}

// SetEmbedResource opts a resource in (enabled=true) or out (enabled=false) of an
// app. Idempotent. Ownership must be checked by the caller.
func (s *Service) SetEmbedResource(appID, resourceType, resourceID string, enabled bool) error {
	if enabled {
		_, err := s.conn.Exec(`
			INSERT INTO embed_resource (embed_app_id, resource_type, resource_id)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, appID, resourceType, resourceID)
		return err
	}
	_, err := s.conn.Exec(`
		DELETE FROM embed_resource
		WHERE embed_app_id = $1 AND resource_type = $2 AND resource_id = $3`,
		appID, resourceType, resourceID)
	return err
}

// ResolveEmbedKey answers the Launch edge's gate question in one round-trip:
// given a publishable key, an Origin, and a target resource, is the key valid and
// is that origin + resource permitted? A missing key yields (nil, nil) so the
// caller returns 401 without leaking whether the key exists.
func (s *Service) ResolveEmbedKey(publishableKey, origin, resourceType, resourceID string) (*api.EmbedResolution, error) {
	var res api.EmbedResolution
	err := s.conn.QueryRow(`
		SELECT ea.id, ea.organisation_id, ea.owner_id,
		       EXISTS (SELECT 1 FROM embed_allowed_origin o
		               WHERE o.embed_app_id = ea.id AND o.origin = $2) AS origin_allowed,
		       EXISTS (SELECT 1 FROM embed_resource r
		               WHERE r.embed_app_id = ea.id AND r.resource_type = $3 AND r.resource_id = $4) AS resource_allowed
		FROM embed_app ea
		WHERE ea.publishable_key = $1`,
		publishableKey, origin, resourceType, resourceID,
	).Scan(&res.EmbedAppID, &res.OrganisationID, &res.OwnerID, &res.OriginAllowed, &res.ResourceAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// loadEmbedRelations fills an app's AllowedOrigins and Resources slices.
func (s *Service) loadEmbedRelations(app *api.EmbedApp) error {
	if err := s.conn.Select(&app.AllowedOrigins,
		`SELECT origin FROM embed_allowed_origin WHERE embed_app_id = $1 ORDER BY origin`, app.ID); err != nil {
		return err
	}
	if err := s.conn.Select(&app.Resources,
		`SELECT embed_app_id, resource_type, resource_id, created_at
		 FROM embed_resource WHERE embed_app_id = $1 ORDER BY created_at`, app.ID); err != nil {
		return err
	}
	return nil
}
