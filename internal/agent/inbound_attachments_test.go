package agent

// Unit tests for processInboundAttachments. The function is the
// single point where Launch's base64-encoded inbound binaries become
// blob tokens visible to the LLM, so each branch matters:
//
//   * happy path with N attachments → N markers, N stored blobs, no
//     base64 left in metadata
//   * empty / missing inbound_attachments → no-op
//   * single corrupt entry mixed with valid ones → keep the valid
//   * blob upload error → drop that entry, keep the rest
//   * empty content message → markers become the content

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
)

// stubUploader records every Put call and returns deterministic
// 16-byte handles so token formatting in test assertions stays
// stable. Errors can be programmed per call by setting putErrAt.
type stubUploader struct {
	calls          []stubUploadCall
	putErrAt       map[int]error // call index → error to return
	nextHandleByte byte
}

type stubUploadCall struct {
	scope   persistence.BlobScope
	mime    string
	purpose string
	size    int
}

func newStubUploader() *stubUploader {
	return &stubUploader{
		putErrAt:       map[int]error{},
		nextHandleByte: 0xAA,
	}
}

func (s *stubUploader) PutBlob(scope persistence.BlobScope, content []byte, mime, purpose string, _ *string) ([]byte, []byte, error) {
	idx := len(s.calls)
	s.calls = append(s.calls, stubUploadCall{
		scope:   scope,
		mime:    mime,
		purpose: purpose,
		size:    len(content),
	})
	if err, ok := s.putErrAt[idx]; ok {
		return nil, nil, err
	}
	handle := make([]byte, 16)
	for i := range handle {
		handle[i] = s.nextHandleByte + byte(idx)
	}
	digest := make([]byte, 32)
	return handle, digest, nil
}

// makeAttachmentEntry returns the inbound-attachment shape Launch
// dispatches today. Caller picks content + metadata; defaults track
// the Telegram photo case.
func makeAttachmentEntry(name, mime, kind string, content []byte) map[string]interface{} {
	return map[string]interface{}{
		"name":           name,
		"mime":           mime,
		"kind":           kind,
		"source_id":      "tg-file-id-1",
		"source_kind":    "telegram",
		"content_base64": base64.StdEncoding.EncodeToString(content),
		"size":           len(content),
	}
}

func TestProcessInboundAttachments_HappyPath_TwoAttachments(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()

	msg := &InboundMessage{
		Content: "look at this",
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				makeAttachmentEntry("cat.png", "image/png", "photo", []byte("PNG bytes here")),
				makeAttachmentEntry("notes.pdf", "application/pdf", "document", []byte("PDF bytes here, longer payload for fun")),
			},
		},
	}

	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))

	// Both blobs landed.
	Expect(up.calls).To(HaveLen(2))
	Expect(up.calls[0].scope.OrgID).To(Equal("org-1"))
	Expect(up.calls[0].mime).To(Equal("image/png"))
	Expect(up.calls[0].purpose).To(Equal(persistence.BlobPurposeInbound))
	Expect(up.calls[1].mime).To(Equal("application/pdf"))

	// Base64 stripped from metadata; resolved attachments[] replaces it.
	Expect(msg.Metadata).NotTo(HaveKey("inbound_attachments"))
	resolved, ok := msg.Metadata["attachments"].([]map[string]interface{})
	Expect(ok).To(BeTrue())
	Expect(resolved).To(HaveLen(2))
	Expect(resolved[0]["name"]).To(Equal("cat.png"))
	Expect(resolved[0]["mime"]).To(Equal("image/png"))
	Expect(resolved[0]["blob"]).To(MatchRegexp(`^flo:blob:[0-9a-f]{32}\?size=\d+&type=image%2Fpng$`))
	Expect(resolved[0]["source_kind"]).To(Equal("telegram"))

	// Content carries the markers, separated from the original text
	// by a blank line.
	Expect(msg.Content).To(HavePrefix("look at this\n\n[attached: cat.png "))
	Expect(msg.Content).To(ContainSubstring("[attached: notes.pdf (application/pdf,"))
	// Two markers, one per file.
	Expect(strings.Count(msg.Content, "[attached:")).To(Equal(2))
}

func TestProcessInboundAttachments_NoMetadata_NoOp(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{Content: "hi"}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(up.calls).To(BeEmpty())
	Expect(msg.Content).To(Equal("hi"))
}

func TestProcessInboundAttachments_EmptyList_NoOp(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Content:  "hi",
		Metadata: map[string]interface{}{"inbound_attachments": []interface{}{}},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(up.calls).To(BeEmpty())
	// Empty list still gets stripped so downstream callers don't see
	// the inbound_attachments key at all.
	Expect(msg.Metadata).NotTo(HaveKey("inbound_attachments"))
}

func TestProcessInboundAttachments_CorruptEntry_KeepsValid(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Content: "",
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				"not a map", // garbage; should be skipped
				makeAttachmentEntry("real.png", "image/png", "photo", []byte("bytes")),
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(up.calls).To(HaveLen(1))
	Expect(up.calls[0].mime).To(Equal("image/png"))
	Expect(msg.Content).To(HavePrefix("[attached: real.png"))
}

func TestProcessInboundAttachments_MissingContentBase64_Skipped(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				map[string]interface{}{
					"name": "ghost.png",
					"mime": "image/png",
					// content_base64 missing
				},
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(up.calls).To(BeEmpty())
	Expect(msg.Content).To(Equal(""))
}

func TestProcessInboundAttachments_BlobUploadError_DropsAttachment(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	up.putErrAt[0] = errors.New("blob full")
	msg := &InboundMessage{
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				makeAttachmentEntry("first.png", "image/png", "photo", []byte("a")),
				makeAttachmentEntry("second.png", "image/png", "photo", []byte("b")),
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	// Two calls attempted, only the second succeeds.
	Expect(up.calls).To(HaveLen(2))
	resolved := msg.Metadata["attachments"].([]map[string]interface{})
	Expect(resolved).To(HaveLen(1))
	Expect(resolved[0]["name"]).To(Equal("second.png"))
}

func TestProcessInboundAttachments_EmptyContent_MarkersBecomeContent(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Content: "   ", // whitespace only — treated as empty
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				makeAttachmentEntry("solo.jpg", "image/jpeg", "photo", []byte("img")),
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(msg.Content).To(MatchRegexp(`^\[attached: solo\.jpg `))
	Expect(msg.Content).NotTo(ContainSubstring("\n\n[attached"))
}

func TestProcessInboundAttachments_MissingMime_Defaults(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				map[string]interface{}{
					"name":           "x.bin",
					"content_base64": base64.StdEncoding.EncodeToString([]byte("bytes")),
				},
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OrgScope("org-1"))
	Expect(up.calls).To(HaveLen(1))
	Expect(up.calls[0].mime).To(Equal("application/octet-stream"))
}

func TestProcessInboundAttachments_NoOrgID_DropsBytesWithoutUpload(t *testing.T) {
	// Personal-mode contract: when there's no org_id to scope the
	// blob under (blob_object.org_id is NOT NULL), the function MUST
	// still strip the base64 payload so the raw bytes don't leak
	// downstream as metadata — but no upload happens and no marker
	// is appended. A loud warning is emitted via the logger, but
	// that's verified by the logger's own tests, not here.
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Content: "hi",
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				makeAttachmentEntry("x.png", "image/png", "photo", []byte("a")),
			},
		},
	}
	processInboundAttachments(up, msg, persistence.BlobScope{})
	Expect(up.calls).To(BeEmpty())
	// base64 payload stripped from metadata.
	Expect(msg.Metadata).NotTo(HaveKey("inbound_attachments"))
	// No resolved attachments[] either — nothing was uploaded.
	Expect(msg.Metadata).NotTo(HaveKey("attachments"))
	// Content untouched.
	Expect(msg.Content).To(Equal("hi"))
}

// Personal-mode scope (no organisation, just a user owner) — the
// path personal-mode flows take.
func TestProcessInboundAttachments_OwnerScope_HappyPath(t *testing.T) {
	RegisterTestingT(t)
	up := newStubUploader()
	msg := &InboundMessage{
		Content: "look",
		Metadata: map[string]interface{}{
			"inbound_attachments": []interface{}{
				makeAttachmentEntry("solo.png", "image/png", "photo", []byte("img")),
			},
		},
	}
	processInboundAttachments(up, msg, persistence.OwnerScope("user-andy"))
	Expect(up.calls).To(HaveLen(1))
	Expect(up.calls[0].scope.OrgID).To(Equal(""))
	Expect(up.calls[0].scope.OwnerID).To(Equal("user-andy"))
	Expect(msg.Content).To(ContainSubstring("[attached: solo.png"))
}

func TestHumanSize_AcrossUnits(t *testing.T) {
	RegisterTestingT(t)
	Expect(humanSize(500)).To(Equal("500 B"))
	Expect(humanSize(2048)).To(Equal("2.0 KB"))
	Expect(humanSize(1500000)).To(Equal("1.4 MB"))
	Expect(humanSize(2_500_000_000)).To(Equal("2.3 GB"))
}

func TestFormatBlobToken_MatchesExecutorShape(t *testing.T) {
	RegisterTestingT(t)
	// 16 random-looking bytes → 32 hex chars
	handle := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F,
	}
	token := formatBlobToken(handle, 12345, "audio/mpeg")
	Expect(token).To(Equal(fmt.Sprintf(
		"flo:blob:000102030405060708090a0b0c0d0e0f?size=12345&type=audio%%2Fmpeg")))
}
