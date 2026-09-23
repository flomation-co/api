package http

// Agent avatars are stored as data URLs on the agent row, so they are
// carried by the ordinary agent JSON and need no second, authenticated
// request from an <img> tag.
//
// That convenience is exactly why the value has to be checked here. It
// is attacker-supplied content that the editor renders, so this file is
// the only thing standing between a crafted "avatar" and whatever the
// browser decides to do with it. Three rules, all of which matter:
//
//   - The media type must be one of a fixed raster list. SVG is
//     deliberately absent: an SVG is a document that can carry script,
//     and a data: URL for one is a script-execution primitive dressed
//     as a picture.
//   - The decoded bytes must actually start with that format's magic
//     number. Without this, the type is just a claim, and a caller can
//     label anything image/png.
//   - The whole thing must be small. It rides on every agent read, so
//     an unbounded value is a denial of service against the list.

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// maxAgentAvatarBytes caps the DECODED image. The editor downscales to
// 128x128 before upload, which lands well under this — the cap is here
// for callers that bypass the editor.
const maxAgentAvatarBytes = 256 * 1024

// agentAvatarSignatures maps an accepted media type to the byte prefix
// a file of that type must begin with.
var agentAvatarSignatures = map[string][][]byte{
	"image/png":  {{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}},
	"image/jpeg": {{0xFF, 0xD8, 0xFF}},
	"image/gif":  {[]byte("GIF87a"), []byte("GIF89a")},
	// WEBP is "RIFF" + a 4-byte length + "WEBP", so the signature is
	// checked in two pieces by validateAgentAvatar.
	"image/webp": {[]byte("RIFF")},
}

// validateAgentAvatar checks a data URL and returns an error naming the
// reason it was refused. An empty avatar is valid — it means "no
// avatar", which is how one is removed.
func validateAgentAvatar(avatar string) error {
	if avatar == "" {
		return nil
	}

	const prefix = "data:"
	if !strings.HasPrefix(avatar, prefix) {
		return fmt.Errorf("avatar must be a data URL")
	}

	comma := strings.IndexByte(avatar, ',')
	if comma < 0 {
		return fmt.Errorf("avatar is not a well-formed data URL")
	}

	meta := avatar[len(prefix):comma]
	if !strings.HasSuffix(meta, ";base64") {
		return fmt.Errorf("avatar must be base64 encoded")
	}
	mediaType := strings.ToLower(strings.TrimSuffix(meta, ";base64"))

	signatures, ok := agentAvatarSignatures[mediaType]
	if !ok {
		return fmt.Errorf("avatar must be a PNG, JPEG, GIF or WebP image")
	}

	// Decoding also rejects a payload that is not valid base64, which a
	// permissive decoder would otherwise let through as garbage bytes.
	decoded, err := base64.StdEncoding.DecodeString(avatar[comma+1:])
	if err != nil {
		return fmt.Errorf("avatar is not valid base64")
	}
	if len(decoded) == 0 {
		return fmt.Errorf("avatar is empty")
	}
	if len(decoded) > maxAgentAvatarBytes {
		return fmt.Errorf("avatar must be %d KB or smaller", maxAgentAvatarBytes/1024)
	}

	matched := false
	for _, signature := range signatures {
		if len(decoded) >= len(signature) && string(decoded[:len(signature)]) == string(signature) {
			matched = true
			break
		}
	}
	// A RIFF container is only a WebP if it says so at byte 8.
	if matched && mediaType == "image/webp" {
		matched = len(decoded) >= 12 && string(decoded[8:12]) == "WEBP"
	}
	if !matched {
		return fmt.Errorf("avatar contents do not match the declared %s type", mediaType)
	}

	return nil
}
