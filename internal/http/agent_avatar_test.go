package http

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func dataURL(mediaType string, body []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(body)
}

var (
	pngBytes  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	gifBytes  = []byte("GIF89a....")
	webpBytes = append([]byte("RIFF\x10\x00\x00\x00WEBP"), 0x00)
)

func TestAgentAvatarAcceptsRasterImages(t *testing.T) {
	gomega.RegisterTestingT(t)

	for mediaType, body := range map[string][]byte{
		"image/png":  pngBytes,
		"image/jpeg": jpegBytes,
		"image/gif":  gifBytes,
		"image/webp": webpBytes,
	} {
		gomega.Expect(validateAgentAvatar(dataURL(mediaType, body))).
			To(gomega.Succeed(), "%s should be accepted", mediaType)
	}
}

func TestAgentAvatarAcceptsRemoval(t *testing.T) {
	gomega.RegisterTestingT(t)

	// Clearing the avatar is how one is removed, so an empty value is
	// not an error.
	gomega.Expect(validateAgentAvatar("")).To(gomega.Succeed())
}

func TestAgentAvatarRejectsSVG(t *testing.T) {
	gomega.RegisterTestingT(t)

	// An SVG is a document that can carry script. It renders as a
	// picture, which is exactly why it must never be accepted as one.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	err := validateAgentAvatar(dataURL("image/svg+xml", svg))
	gomega.Expect(err).To(gomega.HaveOccurred())
	gomega.Expect(err.Error()).To(gomega.ContainSubstring("PNG, JPEG, GIF or WebP"))
}

func TestAgentAvatarRejectsAMislabelledPayload(t *testing.T) {
	gomega.RegisterTestingT(t)

	// The declared type is only a claim; the bytes decide. Without the
	// magic-number check, anything at all could be labelled image/png.
	err := validateAgentAvatar(dataURL("image/png", []byte("<html><script>alert(1)</script>")))
	gomega.Expect(err).To(gomega.HaveOccurred())
	gomega.Expect(err.Error()).To(gomega.ContainSubstring("do not match the declared"))

	// A RIFF container that is not a WebP — RIFF also fronts WAV and AVI.
	riffWav := append([]byte("RIFF\x10\x00\x00\x00WAVE"), 0x00)
	gomega.Expect(validateAgentAvatar(dataURL("image/webp", riffWav))).To(gomega.HaveOccurred())
}

func TestAgentAvatarRejectsMalformedURLs(t *testing.T) {
	gomega.RegisterTestingT(t)

	for _, avatar := range []string{
		"https://example.com/avatar.png",
		"data:image/png,notbase64",
		"data:image/png;base64",
		"data:image/png;base64,!!!!not base64!!!!",
		"data:image/png;base64,",
	} {
		gomega.Expect(validateAgentAvatar(avatar)).
			To(gomega.HaveOccurred(), "%q should be refused", avatar)
	}
}

func TestAgentAvatarRejectsAnOversizedImage(t *testing.T) {
	gomega.RegisterTestingT(t)

	// The avatar rides on every agent read, so an unbounded one is a
	// denial of service against the list.
	oversized := append(pngBytes, make([]byte, maxAgentAvatarBytes)...)
	err := validateAgentAvatar(dataURL("image/png", oversized))
	gomega.Expect(err).To(gomega.HaveOccurred())
	gomega.Expect(err.Error()).To(gomega.ContainSubstring("or smaller"))

	atLimit := append(pngBytes, make([]byte, maxAgentAvatarBytes-len(pngBytes))...)
	gomega.Expect(validateAgentAvatar(dataURL("image/png", atLimit))).To(gomega.Succeed())
}

func TestAgentAvatarIgnoresMediaTypeCase(t *testing.T) {
	gomega.RegisterTestingT(t)

	gomega.Expect(validateAgentAvatar(strings.Replace(
		dataURL("image/png", pngBytes), "image/png", "IMAGE/PNG", 1))).To(gomega.Succeed())
}
