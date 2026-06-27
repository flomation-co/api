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
	scope, ok := s.scopeForUser(c)
	if !ok {
		return
	}
	handle, ok := parseBlobHandle(c)
	if !ok {
		return
	}

	content, mime, _, err := s.persistence.GetBlob(scope, handle)
	if errors.Is(err, persistence.ErrBlobNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "blob not found"})
		return
	}
	if err != nil {
		log.WithError(err).Error("blob (public): get failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read failed"})
		return
	}
	// Cache-Control: private — these are user-scoped bytes; we want
	// the browser to cache them between page navigations but never let
	// a shared proxy serve them to a different user.
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(http.StatusOK, mime, content)
}
