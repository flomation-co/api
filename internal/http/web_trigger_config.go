package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// webTriggerConfig is the Web Trigger node's config, read from a flow's latest
// revision. Drives Launch's history orchestration, strict per-verb 405, and
// request-part → field mapping.
type webTriggerConfig struct {
	Found        bool              `json:"found"`
	KeepHistory  bool              `json:"keep_history"`
	MessageField string            `json:"message_field"`
	Methods      []string          `json:"methods"`
	Fields       map[string]string `json:"fields"`
	// AuthMode is how the edge gates the invoke endpoint: "publishable" (require
	// an embed-app key, the secure default) or "public" (open, no key). An unset
	// or unrecognised value projects as "publishable" so a missing config never
	// silently opens an endpoint.
	AuthMode string `json:"auth_mode"`
	// TriggerID is the flow's "web" trigger record id. Launch invokes THIS trigger
	// (not the generic execute path) so the execution starts from the Web Trigger
	// node — otherwise the generic path picks the flow's manual trigger and the
	// wrong (or a stale) entry node, failing with "no start node specified".
	TriggerID string `json:"trigger_id"`
}

// Web Trigger auth modes (mirror of the executor action's auth_mode options and
// Launch's webAuth* constants).
const (
	webAuthPublishable = "publishable"
	webAuthPublic      = "public"
)

// revDataBytes normalises a revision's Data (typed interface{}) to raw JSON
// bytes. A jsonb column scanned into interface{} is []byte (some drivers/paths
// yield json.RawMessage or string); a decoded map arrives from in-memory callers
// and tests. Only the map case needs re-marshalling — the byte/string cases are
// already JSON and must NOT be marshalled (that would base64-encode them).
func revDataBytes(data interface{}) []byte {
	switch d := data.(type) {
	case nil:
		return []byte("null")
	case []byte:
		return d
	case json.RawMessage:
		return d
	case string:
		return []byte(d)
	default:
		b, _ := json.Marshal(d)
		return b
	}
}

func webTriggerInputValue(inputs []map[string]interface{}, name string) interface{} {
	for _, in := range inputs {
		if n, _ := in["name"].(string); n == name {
			return in["value"]
		}
	}
	return nil
}

func webTriggerTruthy(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	}
	return false
}

// getWebTriggerConfigInternal returns the Web Trigger node's config from the
// flow's latest revision. Internal (mTLS). Returns {found:false} when the flow
// has no Web Trigger, so callers can treat it as a non-Web-Trigger flow.
// GET /api/v1/internal/flo/:FloID/web-trigger
func (s *Service) getWebTriggerConfigInternal(c *gin.Context) {
	rev, err := s.persistence.GetLatestRevisionByFloID(c.Param("FloID"))
	if err != nil || rev == nil {
		c.JSON(http.StatusOK, webTriggerConfig{Found: false})
		return
	}
	var revData struct {
		Nodes []struct {
			Data struct {
				Label  string `json:"label"`
				Config struct {
					Inputs []map[string]interface{} `json:"inputs"`
				} `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}
	// rev.Data is interface{} — a jsonb column scanned into it arrives as raw
	// []byte (or json.RawMessage/string), NOT a decoded map. Marshalling []byte
	// would base64-encode it and the node list would vanish, so normalise to raw
	// JSON bytes first. A decoded map (e.g. from tests) still needs a marshal.
	raw := revDataBytes(rev.Data)
	_ = json.Unmarshal(raw, &revData)

	for _, n := range revData.Nodes {
		if n.Data.Label != "trigger/web" {
			continue
		}
		inputs := n.Data.Config.Inputs
		cfg := webTriggerConfig{Found: true, MessageField: "message", Fields: map[string]string{}, AuthMode: webAuthPublishable}
		// Reconciled with the concurrently-merged `auth` (none/publishable) work:
		// auth_mode is the canonical field. Accept a legacy `auth` value ("none" →
		// public) so any flow saved against the interim `auth` input still resolves.
		if am, _ := webTriggerInputValue(inputs, "auth_mode").(string); strings.TrimSpace(am) == webAuthPublic {
			cfg.AuthMode = webAuthPublic
		} else if a, _ := webTriggerInputValue(inputs, "auth").(string); a == "none" {
			cfg.AuthMode = webAuthPublic
		}
		cfg.KeepHistory = webTriggerTruthy(webTriggerInputValue(inputs, "keep_history"))
		if mf, _ := webTriggerInputValue(inputs, "message_field").(string); strings.TrimSpace(mf) != "" {
			cfg.MessageField = mf
		}
		if m, _ := webTriggerInputValue(inputs, "methods").(string); m != "" {
			for _, v := range strings.Split(m, ",") {
				if v = strings.ToUpper(strings.TrimSpace(v)); v != "" {
					cfg.Methods = append(cfg.Methods, v)
				}
			}
		}
		if f, _ := webTriggerInputValue(inputs, "fields").(string); f != "" {
			var fm map[string]string
			if json.Unmarshal([]byte(f), &fm) == nil {
				cfg.Fields = fm
			}
		}
		// Resolve the flow's "web" trigger record so Launch can invoke it directly
		// (entry = the Web Trigger node) rather than the generic execute path.
		if triggers, terr := s.persistence.GetTriggersByFloID(c.Param("FloID")); terr == nil {
			for _, t := range triggers {
				if t != nil && t.TypeName == "web" {
					cfg.TriggerID = t.ID
					break
				}
			}
		}
		c.JSON(http.StatusOK, cfg)
		return
	}
	c.JSON(http.StatusOK, webTriggerConfig{Found: false})
}
