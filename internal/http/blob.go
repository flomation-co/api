package http

// HTTP layer for the blob store service. These endpoints are
// mTLS-gated and registered ONLY on the internal Gin engine — they
// must never be reachable from public traffic. The caller's org_id
// is supplied via the X-Flomation-Org-Id header (same convention as
// X-Flomation-Parent-Execution-Id used by the execution-hierarchy
// plumbing) — the mTLS cert authenticates the *service*, not the
// tenant, so the service has to declare the tenant on each call.
//
// Three contract details worth being explicit about:
//
//   * Cross-org reads collapse to 404, never 403. The persistence
//     layer returns ErrBlobNotFound in both "row missing" and "row
//     owned by another org" cases so the HTTP layer cannot leak
//     existence even by accident.
//   * The upload response carries the blob *token* in the canonical
//     `flo:blob:<hex>?size=N&type=mime` format so the caller can
//     stash it straight into trigger data or tool output without
//     formatting it themselves.
//   * Uploads always set Content-Type to the *stored* mime on
//     download, not the caller-supplied mime — protects against a
//     mime sniff disagreement at upload time being papered over on
//     read.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// OrgIDHeader is the header carrying the calling service's org
// context on every blob request when the resource is
// organisation-scoped. Mirrors ParentExecutionHeader's role on the
// execution-hierarchy path.
const OrgIDHeader = "X-Flomation-Org-Id"

// OwnerIDHeader is the personal-mode counterpart of OrgIDHeader.
// Used by flows / agents that aren't owned by any organisation.
// Exactly one of (OrgIDHeader, OwnerIDHeader) must be set per request.
const OwnerIDHeader = "X-Flomation-Owner-Id"

// BlobTokenPrefix matches the executor's blobstore constant so the
// returned token round-trips through ParseBlobToken on the consumer
// side without translation. Kept verbatim here to avoid an
// API→executor import cycle.
const blobTokenPrefix = "flo:blob:"

// formatBlobToken renders the verbose-format token the executor's
// blobstore.go expects: flo:blob:<hex>?size=N&type=<url-encoded-mime>
func formatBlobToken(handle []byte, size int64, mime string) string {
	return fmt.Sprintf("%s%s?size=%d&type=%s",
		blobTokenPrefix,
		hex.EncodeToString(handle),
		size,
		url.QueryEscape(mime))
}

// resolveBlobScope reads the BlobScope from the request headers,
// enforcing the "exactly one of OrgID / OwnerID" invariant. Returns
// a zero scope and a written 400 response when zero or both are set.
//
// Org-mode is the common case (channels are organisation-owned by
// default); owner-mode is the personal-mode fallback used by flows
// that don't have an organisation context. The header pair is open
// for use by Launch (inbound dispatch), the executor (M1 tool-output
// storage) and any future internal caller — the auth boundary is
// the same.
func resolveBlobScope(c *gin.Context) persistence.BlobScope {
	orgID := strings.TrimSpace(c.GetHeader(OrgIDHeader))
	ownerID := strings.TrimSpace(c.GetHeader(OwnerIDHeader))
	if orgID == "" && ownerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "one of " + OrgIDHeader + " or " + OwnerIDHeader + " required",
		})
		return persistence.BlobScope{}
	}
	if orgID != "" && ownerID != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "set only one of " + OrgIDHeader + " or " + OwnerIDHeader,
		})
		return persistence.BlobScope{}
	}
	if orgID != "" {
		return persistence.OrgScope(orgID)
	}
	return persistence.OwnerScope(ownerID)
}

// parseBlobHandle decodes the URL :handle param from hex into the
// raw 16 bytes the persistence layer stores. Any decode failure is
// surfaced as 404, not 400 — the path is opaque to the caller and
// "invalid handle" and "unknown handle" should be indistinguishable.
func parseBlobHandle(c *gin.Context) ([]byte, bool) {
	raw := c.Param("handle")
	handle, err := hex.DecodeString(raw)
	if err != nil || len(handle) != persistence.BlobHandleByteLen {
		c.JSON(http.StatusNotFound, gin.H{"error": "blob not found"})
		return nil, false
	}
	return handle, true
}

// putBlobInternal handles POST /api/v1/internal/blob.
//
// Body: multipart/form-data
//   - file       (file)      the bytes to store (≤ 25 MB)
//   - mime       (text)      declared MIME type
//   - purpose    (text)      one of: inbound, tool_output, manual
//   - execution_id (text, optional) ties the blob to an execution row;
//     when set, cascades on execution deletion
//
// Header: X-Flomation-Org-Id (required)
//
// On success returns 201 with the canonical blob token + metadata.
func (s *Service) putBlobInternal(c *gin.Context) {
	scope := resolveBlobScope(c)
	if scope.IsZero() {
		return
	}

	// 25 MB upper bound on the entire request to short-circuit DoS
	// attempts before they touch disk. Add headroom for the multipart
	// boundary + form fields.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		persistence.BlobMaxSizeBytes+1024*1024)

	if err := c.Request.ParseMultipartForm(persistence.BlobMaxSizeBytes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parse multipart: " + err.Error()})
		return
	}

	mime := strings.TrimSpace(c.PostForm("mime"))
	purpose := strings.TrimSpace(c.PostForm("purpose"))
	if mime == "" || purpose == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mime and purpose required"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file part missing: " + err.Error()})
		return
	}
	if fileHeader.Size > persistence.BlobMaxSizeBytes {
		c.JSON(http.StatusRequestEntityTooLarge,
			gin.H{"error": fmt.Sprintf("file exceeds %d-byte cap", persistence.BlobMaxSizeBytes)})
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open upload: " + err.Error()})
		return
	}
	defer func() { _ = f.Close() }()

	content, err := io.ReadAll(io.LimitReader(f, persistence.BlobMaxSizeBytes+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read upload: " + err.Error()})
		return
	}
	if int64(len(content)) > persistence.BlobMaxSizeBytes {
		c.JSON(http.StatusRequestEntityTooLarge,
			gin.H{"error": fmt.Sprintf("file exceeds %d-byte cap", persistence.BlobMaxSizeBytes)})
		return
	}

	// MIME sniff cross-check: detect from bytes and reject if the
	// detected category disagrees with the declared mime. Catches
	// caller mistakes and trivially-disguised payloads. We compare on
	// the category prefix ("image/", "audio/", …) so e.g. declaring
	// image/png for an image/jpeg upload doesn't fail unnecessarily.
	detected := http.DetectContentType(content)
	if !mimeCategoryMatches(detected, mime) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":    "declared mime disagrees with content",
			"declared": mime,
			"detected": detected,
		})
		return
	}

	var execIDPtr *string
	if execID := strings.TrimSpace(c.PostForm("execution_id")); execID != "" {
		execIDPtr = &execID
	}

	handle, _, err := s.persistence.PutBlob(scope, content, mime, purpose, execIDPtr)
	switch {
	case err == nil:
		// fall through
	case errors.Is(err, persistence.ErrBlobInvalidPurpose):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, persistence.ErrBlobQuotaExceeded):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	default:
		log.WithError(err).Error("blob: put failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store failed"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"handle":     hex.EncodeToString(handle),
		"blob_token": formatBlobToken(handle, int64(len(content)), mime),
		"size":       len(content),
		"mime":       mime,
		"purpose":    purpose,
	})
}

// getBlobInternal handles GET /api/v1/internal/blob/:handle.
//
// Streams bytes with the *stored* Content-Type. Cross-org reads and
// unknown handles both return 404.
func (s *Service) getBlobInternal(c *gin.Context) {
	scope := resolveBlobScope(c)
	if scope.IsZero() {
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
		log.WithError(err).Error("blob: get failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read failed"})
		return
	}
	c.Data(http.StatusOK, mime, content)
}

// headBlobInternal handles HEAD /api/v1/internal/blob/:handle. Some
// callers (e.g. the editor's media inspector via a future proxy)
// want to verify a blob exists and learn its size before paying the
// transfer cost. We respond with the metadata as JSON in the body —
// the gin HEAD route serves headers only, but the same handler also
// answers GET .../metadata for explicit metadata pulls.
func (s *Service) headBlobInternal(c *gin.Context) {
	scope := resolveBlobScope(c)
	if scope.IsZero() {
		return
	}
	handle, ok := parseBlobHandle(c)
	if !ok {
		return
	}
	meta, err := s.persistence.HeadBlob(scope, handle)
	if errors.Is(err, persistence.ErrBlobNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "blob not found"})
		return
	}
	if err != nil {
		log.WithError(err).Error("blob: head failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "head failed"})
		return
	}
	c.JSON(http.StatusOK, meta)
}

// deleteBlobInternal handles DELETE /api/v1/internal/blob/:handle.
// Used by callers that want to drop a blob early after a failed retry
// rather than waiting for the TTL sweep.
func (s *Service) deleteBlobInternal(c *gin.Context) {
	scope := resolveBlobScope(c)
	if scope.IsZero() {
		return
	}
	handle, ok := parseBlobHandle(c)
	if !ok {
		return
	}
	err := s.persistence.DeleteBlob(scope, handle)
	if errors.Is(err, persistence.ErrBlobNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "blob not found"})
		return
	}
	if err != nil {
		log.WithError(err).Error("blob: delete failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

// mimeCategoryMatches returns true when the declared and detected
// MIME types share the same top-level category — image/*, audio/*,
// video/*, text/*, application/*. Allows benign disagreements
// (image/png vs image/jpeg) through while catching category swaps
// (image/png claimed for an application/x-executable upload).
//
// Three exceptions:
//
//  1. Declared "application/octet-stream" is always accepted (we
//     trust the caller saying "I don't know").
//
//  2. Detected "application/octet-stream" — http.DetectContentType's
//     default for unrecognised binary — is also accepted to avoid
//     false rejections for legitimate but unusual formats.
//
//  3. Declared media (audio/*, image/*, video/*, application/pdf)
//     paired with a text/* detection is allowed. This covers the
//     executor's TokeniseLargeOutputs path: action outputs like
//     `audio_base64` carry base64-encoded media which is bytewise
//     ASCII and sniffs as text/plain, even though it's semantically
//     audio/image/video. Without this exception EVERY base64-media
//     off-load returns 400 and the agent loop loses access to the
//     manifest entry — which manifested as AI hallucination of fake
//     handles in executions 9dcf8bc3, ee749f82 etc.
func mimeCategoryMatches(detected, declared string) bool {
	if declared == "application/octet-stream" {
		return true
	}
	if strings.HasPrefix(detected, "application/octet-stream") {
		return true
	}
	if isMediaMime(declared) && strings.HasPrefix(detected, "text/") {
		return true
	}
	dc := mimeCategory(detected)
	dl := mimeCategory(declared)
	return dc == dl
}

// isMediaMime returns true for MIME types whose content is commonly
// base64-encoded before transport. The category check above relies
// on this to recognise the "I'm uploading base64-encoded audio /
// image / video" case where bytes sniff as text but the declared
// type is the post-decode semantic type.
func isMediaMime(m string) bool {
	if strings.HasPrefix(m, "audio/") ||
		strings.HasPrefix(m, "image/") ||
		strings.HasPrefix(m, "video/") {
		return true
	}
	return m == "application/pdf"
}

func mimeCategory(m string) string {
	if i := strings.Index(m, "/"); i >= 0 {
		return strings.ToLower(m[:i])
	}
	return strings.ToLower(m)
}
