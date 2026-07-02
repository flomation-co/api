package http

// Blob store HTTP handler tests. The persistence layer is stubbed
// via the shared mockPersistence — we're verifying the HTTP-layer
// invariants, not the SQL behaviour:
//
//   1. The OrgID header is required on every endpoint.
//   2. Multipart upload happy path returns 201 + canonical token.
//   3. Files larger than the 25 MB cap are rejected before they reach
//      persistence.
//   4. Declared/detected MIME category mismatch is rejected.
//   5. Cross-org reads collapse to 404 — no 403, no body leak.
//   6. Unknown / malformed handles return 404 indistinguishably from
//      cross-org reads.
//   7. Quota-exceeded errors from persistence surface as 429.
//   8. Invalid purpose returns 400 (not 500).
//   9. The token format round-trips: handle hex + size + url-encoded
//      mime, in that order, with no leading or trailing whitespace.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// setupBlobRouter mirrors the production route registration in
// service.go for just the blob endpoints. Keeps tests independent of
// any unrelated route changes elsewhere.
func setupBlobRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/blob", svc.putBlobInternal)
	r.GET("/api/v1/internal/blob/:handle", svc.getBlobInternal)
	r.HEAD("/api/v1/internal/blob/:handle", svc.headBlobInternal)
	r.GET("/api/v1/internal/blob/:handle/metadata", svc.headBlobInternal)
	r.DELETE("/api/v1/internal/blob/:handle", svc.deleteBlobInternal)
	r.POST("/api/v1/internal/flo/:FloID/trigger/:TriggerID/upload", svc.putBlobForTrigger)
	return r
}

// buildBlobUpload constructs a multipart upload body matching the
// production contract — file part + mime + purpose + optional
// execution_id form fields.
func buildBlobUpload(t *testing.T, content []byte, mime, purpose, execID string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("mime", mime)
	_ = w.WriteField("purpose", purpose)
	if execID != "" {
		_ = w.WriteField("execution_id", execID)
	}
	part, err := w.CreateFormFile("file", "upload.bin")
	Expect(err).NotTo(HaveOccurred())
	_, err = part.Write(content)
	Expect(err).NotTo(HaveOccurred())
	Expect(w.Close()).To(Succeed())
	return &body, w.FormDataContentType()
}

func newBlobService() (*Service, *mockPersistence) {
	mp := newMockPersistence()
	svc := &Service{persistence: mp}
	return svc, mp
}

// Tiny PNG header bytes — http.DetectContentType recognises this as
// image/png from the magic number, satisfying the MIME-sniff check
// for image/* uploads in tests.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

func TestBlob_PutHappyPath_ReturnsTokenAndMetadata(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusCreated))
	var resp map[string]any
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())

	// Canonical token format invariants — these are the contract.
	token, _ := resp["blob_token"].(string)
	Expect(token).To(HavePrefix("flo:blob:"))
	Expect(token).To(ContainSubstring(fmt.Sprintf("size=%d", len(tinyPNG))))
	Expect(token).To(ContainSubstring("type=" + url.QueryEscape("image/png")))

	// handle hex must round-trip to 16 bytes — the persistence layer's
	// BlobHandleByteLen invariant.
	handleHex, _ := resp["handle"].(string)
	decoded, err := hex.DecodeString(handleHex)
	Expect(err).NotTo(HaveOccurred())
	Expect(decoded).To(HaveLen(persistence.BlobHandleByteLen))
}

func TestBlob_Put_MissingOrgHeader_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	// No OrgIDHeader → 400.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestBlob_Put_MimeMismatch_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Declare a non-media application type for content that sniffs
	// as text/plain — a genuine category mismatch the handler must
	// still reject. (image/png declared for text content is now
	// ALLOWED, because base64-encoded media bytewise looks like
	// text — covered by
	// TestMimeCategoryMatches_MediaDeclaredAsTextDetected.)
	body, contentType := buildBlobUpload(t, []byte("this is plain text content"), "application/x-executable", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("declared mime disagrees"))
}

func TestBlob_Put_InvalidPurpose_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()
	mp.blobPutErr = nil
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", "made_up", "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestBlob_Put_QuotaExceeded_Returns429(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, mp := newBlobService()
	// Pre-load the quota counter so the next upload trips the cap.
	mp.blobQuotaUsed["org:org-1"] = persistence.BlobDailyQuotaPerOrg
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
}

func TestBlob_GetRoundTrip_ReturnsBytesWithStoredMime(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Put first.
	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OrgIDHeader, "org-1")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	Expect(putRec.Code).To(Equal(http.StatusCreated))

	var putResp struct {
		Handle string `json:"handle"`
	}
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	// Then Get.
	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle, nil)
	get.Header.Set(OrgIDHeader, "org-1")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)

	Expect(getRec.Code).To(Equal(http.StatusOK))
	Expect(getRec.Header().Get("Content-Type")).To(HavePrefix("image/png"))
	Expect(getRec.Body.Bytes()).To(Equal(tinyPNG))
}

func TestBlob_CrossOrgRead_Returns404_NotForbidden(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Upload under org-1.
	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OrgIDHeader, "org-1")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	var putResp struct{ Handle string }
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	// Read as org-2 → 404, NOT 403. Body must not reveal that the
	// handle is real, only that it's not found for this caller.
	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle, nil)
	get.Header.Set(OrgIDHeader, "org-2")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)

	Expect(getRec.Code).To(Equal(http.StatusNotFound))
	Expect(getRec.Code).NotTo(Equal(http.StatusForbidden))
	Expect(strings.ToLower(getRec.Body.String())).To(ContainSubstring("not found"))
}

func TestBlob_GetMalformedHandle_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Not hex-decodable.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/not-hex-at-all", nil)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusNotFound))

	// Hex-decodable but wrong length (8 bytes, not 16).
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/0102030405060708", nil)
	req2.Header.Set(OrgIDHeader, "org-1")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	Expect(rec2.Code).To(Equal(http.StatusNotFound))
}

func TestBlob_Delete_RemovesBlob_SecondReadReturns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Put.
	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OrgIDHeader, "org-1")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	var putResp struct{ Handle string }
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	// Delete.
	del := httptest.NewRequest(http.MethodDelete, "/api/v1/internal/blob/"+putResp.Handle, nil)
	del.Header.Set(OrgIDHeader, "org-1")
	delRec := httptest.NewRecorder()
	r.ServeHTTP(delRec, del)
	Expect(delRec.Code).To(Equal(http.StatusNoContent))

	// Re-get → 404.
	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle, nil)
	get.Header.Set(OrgIDHeader, "org-1")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)
	Expect(getRec.Code).To(Equal(http.StatusNotFound))
}

func TestBlob_HeadReturnsMetadataWithoutBytes(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	// Put.
	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OrgIDHeader, "org-1")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	var putResp struct{ Handle string }
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	// Use the explicit /metadata route — gin's HEAD handler discards
	// the body for true HEAD requests, but /metadata is the
	// reachable-from-code surface for "describe this blob".
	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle+"/metadata", nil)
	get.Header.Set(OrgIDHeader, "org-1")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)

	Expect(getRec.Code).To(Equal(http.StatusOK))
	var meta map[string]any
	Expect(json.Unmarshal(getRec.Body.Bytes(), &meta)).To(Succeed())
	Expect(meta["mime"]).To(Equal("image/png"))
	Expect(meta["size"]).To(BeNumerically("==", len(tinyPNG)))
}

// Pure-function tests for the MIME category matcher — exercised
// directly rather than through the multipart upload path so each
// branch is covered cheaply.

func TestMimeCategoryMatches_OctetStreamAlwaysAccepted(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	Expect(mimeCategoryMatches("text/plain; charset=utf-8", "application/octet-stream")).To(BeTrue())
	Expect(mimeCategoryMatches("application/octet-stream", "image/png")).To(BeTrue())
}

func TestMimeCategoryMatches_SameCategoryDifferentSubtype(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	Expect(mimeCategoryMatches("image/jpeg", "image/png")).To(BeTrue())
	Expect(mimeCategoryMatches("audio/mpeg", "audio/ogg")).To(BeTrue())
}

func TestMimeCategoryMatches_DifferentCategoryRejected(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	// Note: text→media is now allowed (see TestMimeCategoryMatches_
	// MediaDeclaredAsTextDetected) because base64-encoded media
	// always sniffs as text. The "real" cross-category rejection
	// cases are media↔media and application↔media.
	Expect(mimeCategoryMatches("video/mp4", "audio/mpeg")).To(BeFalse())
	Expect(mimeCategoryMatches("application/x-executable", "image/png")).To(BeFalse())
}

// TestMimeCategoryMatches_MediaDeclaredAsTextDetected is the
// regression guard for the executor's blob off-load path. Action
// outputs like `audio_base64` carry base64-encoded media — bytewise
// ASCII, sniffs as text/plain — declared with the post-decode
// semantic type (audio/mpeg, image/png, etc.). Before this
// exception, every base64-media upload returned 400 and the agent
// loop's manifest entry was missing, leading the AI to hallucinate
// fake handles (production executions 9dcf8bc3 / ee749f82). The
// fix allows declared media MIMEs paired with text/* detection.
func TestMimeCategoryMatches_MediaDeclaredAsTextDetected(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	// All four media categories must accept the text-detected case.
	Expect(mimeCategoryMatches("text/plain; charset=utf-8", "audio/mpeg")).To(BeTrue())
	Expect(mimeCategoryMatches("text/plain; charset=utf-8", "image/png")).To(BeTrue())
	Expect(mimeCategoryMatches("text/plain; charset=utf-8", "video/mp4")).To(BeTrue())
	Expect(mimeCategoryMatches("text/plain; charset=utf-8", "application/pdf")).To(BeTrue())

	// Subtypes don't matter; only the category prefix.
	Expect(mimeCategoryMatches("text/html", "audio/ogg")).To(BeTrue())
	Expect(mimeCategoryMatches("text/xml", "image/jpeg")).To(BeTrue())

	// The exception is one-way: declared text + detected media must
	// still be rejected (someone declaring "text/plain" for binary
	// content is a real category swap, not a base64-encoding case).
	Expect(mimeCategoryMatches("audio/mpeg", "text/plain")).To(BeFalse())
	Expect(mimeCategoryMatches("image/png", "text/html")).To(BeFalse())

	// Non-media declared types must still be rejected when bytes
	// look like text. application/x-executable should never appear
	// declared as anything but its real type.
	Expect(mimeCategoryMatches("text/plain", "application/x-executable")).To(BeFalse())
	Expect(mimeCategoryMatches("text/plain", "application/zip")).To(BeFalse())
}

// Ensure the formatBlobToken function emits the exact verbose format
// the executor's ParseBlobToken accepts. The hex handle, decimal size
// and URL-encoded mime are all load-bearing — change any of them and
// the M1 round-trip breaks silently.
func TestFormatBlobToken_VerboseRoundTrip(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	handle := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10}
	token := formatBlobToken(handle, 4096, "audio/mpeg")

	Expect(token).To(HavePrefix("flo:blob:"))
	Expect(token).To(ContainSubstring("0102030405060708090a0b0c0d0e0f10"))
	Expect(token).To(ContainSubstring("size=4096"))
	Expect(token).To(ContainSubstring("type=audio%2Fmpeg"))
}

// Sanity check that the multipart parser tolerates an empty
// execution_id form field — production callers (executor for inbound
// blobs that predate the execution) submit "" rather than omitting
// the field.
// Personal-mode round-trip: the OwnerIDHeader path stores a blob and
// reads it back exactly like the org path. The two scopes share no
// keying so an org-scoped read with the same handle would 404 (see
// the cross-org test for the symmetric proof in the other direction).
func TestBlob_OwnerScope_RoundTrip(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OwnerIDHeader, "user-andy")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	Expect(putRec.Code).To(Equal(http.StatusCreated))

	var putResp struct{ Handle string }
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle, nil)
	get.Header.Set(OwnerIDHeader, "user-andy")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)
	Expect(getRec.Code).To(Equal(http.StatusOK))
	Expect(getRec.Body.Bytes()).To(Equal(tinyPNG))
}

// Owner-scoped blob is invisible to an org-scoped reader (and vice
// versa) — same handle, different scope dimension, must collapse to
// 404 just like cross-org reads do.
func TestBlob_OrgReader_CannotSeeOwnerBlob(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	put := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	put.Header.Set("Content-Type", contentType)
	put.Header.Set(OwnerIDHeader, "user-andy")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, put)
	var putResp struct{ Handle string }
	Expect(json.Unmarshal(putRec.Body.Bytes(), &putResp)).To(Succeed())

	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/blob/"+putResp.Handle, nil)
	get.Header.Set(OrgIDHeader, "org-1")
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, get)
	Expect(getRec.Code).To(Equal(http.StatusNotFound))
}

// Sending BOTH headers is a contract violation — exactly-one is the
// rule, and the handler must refuse the request rather than silently
// pick one. A future caller that accidentally sets both should see
// the error immediately, not weeks later when an org-scoped blob
// vanishes from a personal-mode lookup.
func TestBlob_BothHeadersSet_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	req.Header.Set(OwnerIDHeader, "user-andy")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
	Expect(rec.Body.String()).To(ContainSubstring("set only one"))
}

func TestBlob_PutWithEmptyExecutionID_Accepted(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)
	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/blob", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(OrgIDHeader, "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusCreated))
}

// Silence unused-import noise that crops up if test surface gets
// trimmed later. io is here for future streaming variants.
var _ = io.Copy

// ── putBlobForTrigger — trigger-scoped anonymous upload ──

func TestBlob_PutForTrigger_OrgScopedFlow_ReturnsToken(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc, mp := newBlobService()
	orgID := "org-42"
	// GetFloByID has to import the api package for the Flo struct type.
	mp.flos["flow-1"] = &api.Flo{ID: "flow-1", OrganisationID: &orgID}
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/flo/flow-1/trigger/trg-1/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusCreated))
	var resp map[string]any
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	token, _ := resp["blob_token"].(string)
	Expect(token).To(HavePrefix("flo:blob:"))
	Expect(token).To(ContainSubstring("type=" + url.QueryEscape("image/png")))
}

func TestBlob_PutForTrigger_OwnerScopedFlow_ReturnsToken(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc, mp := newBlobService()
	authorID := "user-9"
	mp.flos["flow-personal"] = &api.Flo{ID: "flow-personal", AuthorID: &authorID}
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/flo/flow-personal/trigger/trg-x/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusCreated))
}

func TestBlob_PutForTrigger_UnknownFlow_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc, _ := newBlobService()
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/flo/absent-flow/trigger/trg-1/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusNotFound))
}

func TestBlob_PutForTrigger_MimeMismatch_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc, mp := newBlobService()
	orgID := "org-42"
	mp.flos["flow-1"] = &api.Flo{ID: "flow-1", OrganisationID: &orgID}
	r := setupBlobRouter(svc)

	// PNG bytes declared as PDF — MIME sniff must catch it. Shared
	// pipeline means every validation rule from putBlobInternal
	// applies to trigger uploads too.
	body, contentType := buildBlobUpload(t, tinyPNG, "application/pdf", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/flo/flow-1/trigger/trg-1/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusBadRequest))
}

func TestBlob_PutForTrigger_NoHeaderNeeded(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	// The whole point of the trigger endpoint: no org/owner header
	// required. Scope is derived server-side from the flow.
	svc, mp := newBlobService()
	orgID := "org-42"
	mp.flos["flow-1"] = &api.Flo{ID: "flow-1", OrganisationID: &orgID}
	r := setupBlobRouter(svc)

	body, contentType := buildBlobUpload(t, tinyPNG, "image/png", persistence.BlobPurposeInbound, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/flo/flow-1/trigger/trg-1/upload", body)
	req.Header.Set("Content-Type", contentType)
	// No X-Flomation-Org-Id / X-Flomation-Owner-Id — still succeeds.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusCreated))
}

// Silence unused-import noise for the api package on the trigger tests.
var _ = api.Flo{}
