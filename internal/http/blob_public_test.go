package http

// Tests for the JWT-protected public blob fetch endpoint. We're
// pinning the auth invariants — scope is derived from the JWT user
// and persistence-layer scope mismatches collapse to 404 — not the
// SQL behaviour itself (which has its own tests in the persistence
// package).
//
// The invariants under test:
//
//   1. Org-mode user: their org's blob → 200 with stored bytes/mime.
//   2. Org-mode user: a blob from a DIFFERENT org → 404 (cross-tenant
//      isolation; no 403, no body leak).
//   3. Personal-mode user: their own owner-scoped blob → 200.
//   4. Personal-mode user: org-scoped blob (even with handle in hand)
//      → 404.
//   5. Malformed/short handle → 404 (mirrors internal handler).
//   6. Missing user context (gin.Set never called) → 401.
//   7. The Cache-Control header is set to private — no shared-proxy
//      cross-user caching.

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// setupBlobPublicRouter wires the public blob route with a synthetic
// JWT middleware that injects a pre-seeded user_id into the gin
// context. The real jwtMiddleware does the same with a verified
// token; we skip that step here so tests stay focused on the handler.
func setupBlobPublicRouter(svc *Service, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if userID != "" {
			c.Set("account_id", userID)
		}
		c.Next()
	})
	r.GET("/api/v1/blob/:handle", svc.getBlobPublic)
	return r
}

// seedBlob writes a blob into the mockPersistence under the given
// scope and returns the hex handle so the test can request it via the
// HTTP route.
func seedBlob(m *mockPersistence, scope persistence.BlobScope, content []byte, mime string) string {
	// Random handle, same width as the production persistence layer.
	handle := make([]byte, persistence.BlobHandleByteLen)
	for i := range handle {
		handle[i] = byte(rand.IntN(256))
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	m.blobs[blobKey(scope, handle)] = mockBlob{content: cp, mime: mime, purpose: persistence.BlobPurposeToolOutput}
	// Stash the sha256 so HEAD tests on the same blob foot if added later.
	_ = sha256.Sum256(content)
	return hex.EncodeToString(handle)
}

// seedOrgUser stores a User row keyed by ID in mockPersistence — the
// public handler resolves it via persistence.GetUserByID when reading
// the gin context's account_id.
func seedOrgUser(m *mockPersistence, userID, orgID string) {
	if m.users == nil {
		m.users = map[string]*api.User{}
	}
	now := time.Now()
	m.users[userID] = &api.User{
		ID:   userID,
		Name: "Test User",
		Organisations: []api.Organisation{
			{ID: orgID, Name: "Test Org", CreatedAt: &now},
		},
	}
}

func seedPersonalUser(m *mockPersistence, userID string) {
	if m.users == nil {
		m.users = map[string]*api.User{}
	}
	m.users[userID] = &api.User{
		ID:   userID,
		Name: "Personal User",
	}
}

// TestBlobPublic_OrgScope_HappyPath confirms the read-your-own-org-blob
// path returns 200 with the stored Content-Type and bytes. This is the
// load-bearing happy path — without this working, the editor's media
// inspector can't render anything.
func TestBlobPublic_OrgScope_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()

	userID := "user-1"
	orgID := "org-1"
	seedOrgUser(mp, userID, orgID)

	audioBytes := []byte("ID3\x04\x00\x00\x00\x00\x00\x00fake mp3 bytes")
	handle := seedBlob(mp, persistence.OrgScope(orgID), audioBytes, "audio/mpeg")

	r := setupBlobPublicRouter(svc, userID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+handle, nil)
	r.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Header().Get("Content-Type")).To(Equal("audio/mpeg"))
	Expect(w.Body.Bytes()).To(Equal(audioBytes))
	Expect(w.Header().Get("Cache-Control")).To(ContainSubstring("private"))
}

// TestBlobPublic_OrgScope_CrossOrgReadIs404 is the cross-tenant
// invariant. The handle is real, the bytes exist, but the requesting
// user belongs to a DIFFERENT org. Must return 404 — never 403, never
// leak the bytes, never even confirm the blob exists.
func TestBlobPublic_OrgScope_CrossOrgReadIs404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()

	// Blob owned by org A.
	handle := seedBlob(mp, persistence.OrgScope("org-a"), []byte("secret"), "text/plain")

	// User belongs to org B — must NOT see it.
	seedOrgUser(mp, "user-b", "org-b")

	r := setupBlobPublicRouter(svc, "user-b")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+handle, nil)
	r.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
	Expect(w.Body.String()).NotTo(ContainSubstring("secret"))
}

// TestBlobPublic_PersonalScope_HappyPath confirms personal-mode users
// can fetch blobs scoped to their own user_id. This is the path used
// when the executor runs flows for users who have no organisation.
func TestBlobPublic_PersonalScope_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()

	userID := "personal-1"
	seedPersonalUser(mp, userID)

	imageBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x00}
	handle := seedBlob(mp, persistence.OwnerScope(userID), imageBytes, "image/png")

	r := setupBlobPublicRouter(svc, userID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+handle, nil)
	r.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Header().Get("Content-Type")).To(Equal("image/png"))
	Expect(w.Body.Bytes()).To(Equal(imageBytes))
}

// TestBlobPublic_PersonalUser_CannotReachOrgBlob pins the inverse
// isolation: a personal-mode user must not be able to read an
// org-scoped blob by guessing the handle. Personal scope and org
// scope are separate buckets.
func TestBlobPublic_PersonalUser_CannotReachOrgBlob(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()

	handle := seedBlob(mp, persistence.OrgScope("some-org"), []byte("org bytes"), "text/plain")
	seedPersonalUser(mp, "personal-2")

	r := setupBlobPublicRouter(svc, "personal-2")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+handle, nil)
	r.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
}

// TestBlobPublic_MalformedHandle_404 keeps the route's response shape
// consistent. Anything that doesn't parse as a proper handle is 404,
// not 400 — the path is opaque to the caller and "invalid" and
// "unknown" should be indistinguishable.
func TestBlobPublic_MalformedHandle_404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()
	seedOrgUser(mp, "u", "o")

	r := setupBlobPublicRouter(svc, "u")
	for _, bad := range []string{"not-hex", "abcd", "deadbeef"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+bad, nil)
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusNotFound), "expected 404 for handle %q, got %d", bad, w.Code)
	}
}

// TestBlobPublic_MissingUser_401 is a defensive test for the case
// where the route is reached without the JWT middleware setting
// account_id (would indicate a misconfigured route registration, but
// guarding it stops a silent 500 from happening if that ever
// regresses).
func TestBlobPublic_MissingUser_401(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()

	// Pass empty userID — the synthetic middleware skips the c.Set
	// call when userID == "", reproducing the no-JWT scenario.
	r := setupBlobPublicRouter(svc, "")
	w := httptest.NewRecorder()
	// Use a well-formed handle so the 401 path is unambiguously the
	// missing-user case, not a malformed-handle bounce.
	good := hex.EncodeToString(make([]byte, persistence.BlobHandleByteLen))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blob/"+good, nil)
	r.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusUnauthorized))
}
