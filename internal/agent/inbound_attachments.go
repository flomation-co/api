package agent

// Inbound attachment processor — bridges Launch's per-channel parsers
// to the API's blob_object tier. Three responsibilities:
//
//   1. Extract `inbound_attachments` (an array of base64-encoded file
//      entries) from msg.Metadata.
//   2. Upload each binary to blob_object via PutBlob, scoped to the
//      agent's organisation. The base64 is stripped from the metadata
//      at the same time — we do NOT want bytes echoed back to anything
//      downstream of this point.
//   3. Build a structured attachments[] array with blob tokens and
//      append `[attached: name (mime, human-size) → token]` markers
//      to msg.Content so the LLM sees the file references as part of
//      the user's turn.
//
// Failures on individual attachments are logged and dropped — a
// corrupt photo upload shouldn't sink an entire flow. The msg is
// mutated in place because the calling pipeline already passes it by
// value through every step.

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// inboundAttachmentMetadataKey is the metadata field Launch populates
// with the raw inbound binaries. After processing we delete this key
// and write the resolved set under attachmentsMetadataKey instead, so
// downstream consumers never see the base64 payload.
const inboundAttachmentMetadataKey = "inbound_attachments"

// attachmentsMetadataKey is the canonical structured-attachments
// field carried in trigger_data.metadata. Same shape across channels
// — Slack inbound (M3) will populate it identically.
const attachmentsMetadataKey = "attachments"

// BlobUploaderFunc is the narrow shape ApplyInboundAttachments needs.
// Both the persistence service and any test stub satisfy it.
type BlobUploaderFunc interface {
	PutBlob(scope persistence.BlobScope, content []byte, mime, purpose string, execID *string) ([]byte, []byte, error)
}

// processInboundAttachments is a thin wrapper for the legacy and
// agent-orchestrator inbound paths that operate on an InboundMessage.
// Delegates to ApplyInboundAttachments, the shared implementation
// also used by the standalone-trigger path in trigger_dispatch.go.
func processInboundAttachments(uploader BlobUploaderFunc, msg *InboundMessage, scope persistence.BlobScope) {
	if msg == nil {
		return
	}
	msg.Content = ApplyInboundAttachments(uploader, msg.Content, msg.Metadata, scope)
}

// ApplyInboundAttachments is the shared core. It:
//
//  1. Reads `inbound_attachments` (an array of base64-encoded file
//     entries Launch dispatched) from metadata.
//  2. Strips that key unconditionally — the base64 payload must never
//     survive past this point, even when no processing happens.
//  3. Uploads each entry to the blob tier under the given scope (org
//     XOR owner) with purpose='inbound' (→ 30 day TTL).
//  4. Writes the resolved, structured attachments[] back to metadata.
//  5. Returns the original content with `[attached: name (mime,
//     size) → token]` markers appended, separated by a blank line
//     when there was non-empty original text.
//
// When the scope is invalid (neither dimension set), uploads are
// skipped and a warning is logged. The base64 payload is still
// stripped — better to drop the bytes than leak them downstream.
//
// Per-attachment failures (corrupt entry, decode error, blob upload
// error) are logged and dropped so a single bad file can't sink the
// whole message.
func ApplyInboundAttachments(uploader BlobUploaderFunc, content string, metadata map[string]interface{}, scope persistence.BlobScope) string {
	if metadata == nil {
		return content
	}
	rawList, ok := metadata[inboundAttachmentMetadataKey].([]interface{})
	if !ok {
		return content
	}
	// Strip the key even if the list is empty — no inbound base64
	// payload should ever survive past this point, regardless of how
	// many entries it had.
	delete(metadata, inboundAttachmentMetadataKey)
	if len(rawList) == 0 {
		return content
	}
	if !scope.Valid() {
		log.WithField("attachment_count", len(rawList)).
			Warn("inbound attachments: no auth scope (neither org_id nor owner_id); dropping bytes without storage")
		return content
	}

	resolved := make([]map[string]interface{}, 0, len(rawList))
	var markers []string

	for i, raw := range rawList {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			log.WithField("index", i).Warn("inbound attachment: entry not a map; skipping")
			continue
		}
		b64, _ := entry["content_base64"].(string)
		if b64 == "" {
			log.WithField("index", i).Warn("inbound attachment: missing content_base64; skipping")
			continue
		}
		content, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			log.WithError(err).Warn("inbound attachment: base64 decode failed; skipping")
			continue
		}

		mime, _ := entry["mime"].(string)
		if mime == "" {
			mime = "application/octet-stream"
		}
		name, _ := entry["name"].(string)
		if name == "" {
			name = fmt.Sprintf("attachment_%d", i+1)
		}

		handle, _, err := uploader.PutBlob(scope, content, mime, persistence.BlobPurposeInbound, nil)
		if err != nil {
			log.WithFields(log.Fields{
				"name":  name,
				"mime":  mime,
				"error": err,
			}).Warn("inbound attachment: blob upload failed; skipping")
			continue
		}

		token := formatBlobToken(handle, len(content), mime)

		// Structured form for downstream consumers (flow trigger data,
		// future UI). Drop the bytes — only tokens travel from here.
		out := map[string]interface{}{
			"name":        name,
			"mime":        mime,
			"size":        len(content),
			"blob":        token,
			"source_id":   entry["source_id"],
			"source_kind": entry["source_kind"],
		}
		// Preserve optional shape fields when the channel surfaced them.
		passthroughOptional(entry, out, "kind", "width", "height", "duration")
		resolved = append(resolved, out)

		// Marker text the LLM reads inside the user turn. Format is
		// deliberately scannable: square-bracket tag, `→` separator, full
		// token. The agent can parse it visually or programmatically.
		markers = append(markers,
			fmt.Sprintf("[attached: %s (%s, %s) → %s]",
				name, mime, humanSize(int64(len(content))), token))
	}

	if len(resolved) > 0 {
		metadata[attachmentsMetadataKey] = resolved
	}

	// Append markers to content. If the message had no text, the
	// markers ARE the content. A blank line separates original text
	// from the auto-promoted markers so the user's writing remains
	// visually distinct in conversation history.
	if len(markers) > 0 {
		joined := strings.Join(markers, "\n")
		if strings.TrimSpace(content) == "" {
			return joined
		}
		return content + "\n\n" + joined
	}
	return content
}

// passthroughOptional copies any of `keys` from src into dst when the
// source value is present and non-zero. Keeps the resolved-attachment
// shape concise — no nil/zero noise for channels that don't surface
// a given field.
func passthroughOptional(src, dst map[string]interface{}, keys ...string) {
	for _, k := range keys {
		v, ok := src[k]
		if !ok || v == nil {
			continue
		}
		// Treat numeric zero / empty string as "not present" — the
		// channel either omitted the field or sent the default.
		switch t := v.(type) {
		case string:
			if t == "" {
				continue
			}
		case float64:
			if t == 0 {
				continue
			}
		case int:
			if t == 0 {
				continue
			}
		}
		dst[k] = v
	}
}

// formatBlobToken mirrors the executor's blobstore format so any
// store→fetch round-trip produces an identical token. Duplicated
// across executor / API / Launch on purpose — the format is part of
// our public surface and lives in three places intentionally.
func formatBlobToken(handle []byte, size int, mime string) string {
	return fmt.Sprintf("flo:blob:%s?size=%d&type=%s",
		hex.EncodeToString(handle),
		size,
		url.QueryEscape(mime))
}

// humanSize renders a byte count as KB / MB / GB with one decimal
// place. Used only in the [attached: …] marker text — the LLM can
// parse "248 KB" just as well as "254300" but humans skim the prompt
// too.
func humanSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
