package http

// Public (JWT-protected) read endpoint for blob bytes — exposed so the
// editor's execution-viewer can fetch and render media that was
// off-loaded to the blob store via the executor's tokenisation path.
//
// Authorisation model — minimal but correct:
//
//   * Derive a BlobScope from the JWT user's session: org-mode users
//     get OrgScope(<their org>), personal-mode users get
//     OwnerScope(<their user id>). The persistence layer's existing
//     scope check refuses to return blobs that don't belong to the
//     supplied scope — cross-tenant reads collapse to 404, never 403,
//     so we can't leak blob existence even via timing.
//
//   * No per-blob ACL beyond scope. Any user who can read an execution
//     in their org/account can read any blob produced inside it. This
//     matches the existing execution-detail UX (you either have org
//     access or you don't). When RBAC lands, the right next step is to
//     additionally require execution-read permission keyed on the
//     blob's `purpose` and the execution_id it was created against —
//     see persistence.BlobObject.ExecutionID. That refinement is
//     deferred per the comment thread on this feature.
//
//   * The route ALWAYS returns 404 for unknown / cross-scope blobs.
//     Never 403. Mirrors the internal handler — same threat model.

import (
	"errors"
	"net/http"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// userBlobScopes returns every blob scope the user may read: their personal
// owner scope plus one org scope per organisation they belong to. A blob is
// served if it lives in ANY of these.
//
// CRUCIAL: it loads the user's real memberships from the DB (GetMyOrganisations)
// rather than user.Organisations — the latter is populated only from the
// request's active-org context (c.Get("organisation_id")), which the editor's
// plain cookie-authed blob fetch does NOT send. So user.Organisations is empty
// on this path, and relying on it 404'd every org-scoped blob (screenshots,
// chart renders) in the execution view.
func (s *Service) userBlobScopes(userID string) []persistence.BlobScope {
	scopes := []persistence.BlobScope{persistence.OwnerScope(userID)}
	orgs, err := s.persistence.GetMyOrganisations(userID)
	if err != nil {
		log.WithError(err).Warn("blob (public): could not load user organisations; personal scope only")
		return scopes
	}
	for _, org := range orgs {
		if org != nil && org.ID != "" {
			scopes = append(scopes, persistence.OrgScope(org.ID))
		}
	}
	return scopes
}

// scopeForUser builds the BlobScope the JWT user is allowed to query
// against. Returns false (with a written 401) when the JWT context is
// missing or malformed — this shouldn't happen behind jwtMiddleware
// but guards the type-cast either way.
//
// Org mode wins when the user has any org membership — matches
// verifyOrgAccess's semantics in service.go. Personal mode falls back
// to the user's own id as the OwnerScope.
func (s *Service) scopeForUser(c *gin.Context) (persistence.BlobScope, bool) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return persistence.BlobScope{}, false
	}
	if len(user.Organisations) > 0 {
		return persistence.OrgScope(user.Organisations[0].ID), true
	}
	return persistence.OwnerScope(user.ID), true
}

// getBlobPublic handles GET /api/v1/blob/:handle.
//
// Streams the stored bytes back with the stored Content-Type so a
// browser MediaPlayer (audio/video/image) can render the bytes
// directly. Range requests are NOT supported in this first cut — the
// editor's media inspector loads the whole blob into memory anyway
// (it's bounded by persistence.BlobMaxSizeBytes upstream). If that
// becomes a bottleneck for large videos we can add Range support
// without changing the route shape.
func (s *Service) getBlobPublic(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	handle, ok := parseBlobHandle(c)
	if !ok {
		return
	}

	// Try every scope the user can read (personal + each org membership).
	// Cross-scope / unknown blobs fall through to 404 — never 403, and the
	// bytes are never leaked. A miss is a cheap indexed lookup that never
	// decrypts, so the loop is inexpensive even for many memberships.
	for _, scope := range s.userBlobScopes(user.ID) {
		content, mime, _, err := s.persistence.GetBlob(scope, handle)
		if err == nil {
			// Cache-Control: private — user-scoped bytes; cache between page
			// navigations but never let a shared proxy serve another user.
			c.Header("Cache-Control", "private, max-age=300")
			c.Data(http.StatusOK, mime, content)
			return
		}
		if !errors.Is(err, persistence.ErrBlobNotFound) {
			log.WithError(err).Error("blob (public): get failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read failed"})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "blob not found"})
}

// putAssetPublic handles POST /api/v1/asset — a JWT-authed upload of a user's
// flow asset (a logo, PSD template, image, …) that the editor's Asset node
// wires into file-accepting inputs of other nodes.
//
// Body: multipart/form-data — file (≤ 25 MB) + mime. Unlike the internal
// endpoint, the purpose is pinned SERVER-SIDE to BlobPurposeAsset (permanent,
// no TTL) so a client cannot mint a differently-scoped blob via this route. The
// scope is the authenticated user's org (if any) or their own id. Returns 201 +
// the canonical flo:blob: token, which the editor stores in the node config.
//
// Lifetime: these blobs never expire. Orphan cleanup (an asset no longer
// referenced by any live revision) is a separate sweep, not a TTL — see
// PLAN-flow-assets.md.
func (s *Service) putAssetPublic(c *gin.Context) {
	scope, ok := s.scopeForUser(c)
	if !ok {
		return
	}
	s.storeMultipartBlob(c, scope, persistence.BlobPurposeAsset)
}
