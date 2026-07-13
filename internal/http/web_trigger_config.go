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
	raw, _ := json.Marshal(rev.Data)
	_ = json.Unmarshal(raw, &revData)

	for _, n := range revData.Nodes {
		if n.Data.Label != "trigger/web" {
			continue
		}
		inputs := n.Data.Config.Inputs
		cfg := webTriggerConfig{Found: true, MessageField: "message", Fields: map[string]string{}}
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
		c.JSON(http.StatusOK, cfg)
		return
	}
	c.JSON(http.StatusOK, webTriggerConfig{Found: false})
}
