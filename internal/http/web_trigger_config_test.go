package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

type webTrigCfgMock struct {
	mockPersistence
	rev      *api.Revision
	triggers []*api.Trigger
}

func (m *webTrigCfgMock) GetLatestRevisionByFloID(id string) (*api.Revision, error) {
	return m.rev, nil
}

func (m *webTrigCfgMock) GetTriggersByFloID(string) ([]*api.Trigger, error) {
	return m.triggers, nil
}

func webTrigCfgRequest(svc *Service) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/internal/flo/:FloID/web-trigger", svc.getWebTriggerConfigInternal)
	req := httptest.NewRequest(http.MethodGet, "/internal/flo/flow-1/web-trigger", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebTriggerConfig_ReadsNodeConfig(t *testing.T) {
	RegisterTestingT(t)

	revData := map[string]interface{}{
		"nodes": []map[string]interface{}{
			{"data": map[string]interface{}{
				"label": "trigger/web",
				"config": map[string]interface{}{
					"inputs": []map[string]interface{}{
						{"name": "methods", "value": "GET,POST"},
						{"name": "auth_mode", "value": "public"},
						{"name": "keep_history", "value": true},
						{"name": "message_field", "value": "prompt"},
						{"name": "fields", "value": `{"id":"path"}`},
					},
				},
			}},
		},
	}
	mock := &webTrigCfgMock{rev: &api.Revision{Data: revData}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := webTrigCfgRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	body := w.Body.String()
	Expect(body).To(ContainSubstring(`"found":true`))
	Expect(body).To(ContainSubstring(`"keep_history":true`))
	Expect(body).To(ContainSubstring(`"message_field":"prompt"`))
	Expect(body).To(ContainSubstring(`"GET"`))
	Expect(body).To(ContainSubstring(`"POST"`))
	Expect(body).To(ContainSubstring(`"id":"path"`))
	Expect(body).To(ContainSubstring(`"auth_mode":"public"`))
}

// An absent or unrecognised auth_mode projects as the secure "publishable"
// default, so a legacy Web Trigger never silently becomes publicly open.
func TestWebTriggerConfig_DefaultsToPublishable(t *testing.T) {
	RegisterTestingT(t)

	revData := map[string]interface{}{
		"nodes": []map[string]interface{}{
			{"data": map[string]interface{}{
				"label": "trigger/web",
				"config": map[string]interface{}{
					"inputs": []map[string]interface{}{
						{"name": "methods", "value": "POST"},
						// no auth_mode input at all
					},
				},
			}},
		},
	}
	mock := &webTrigCfgMock{rev: &api.Revision{Data: revData}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := webTrigCfgRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"auth_mode":"publishable"`))
}

// Regression: a jsonb `data` column scanned into Revision.Data (interface{})
// arrives as raw JSON bytes, NOT a decoded map. The handler must parse those
// bytes directly — marshalling them would base64-encode the payload and drop
// every node, silently yielding {found:false} (which degraded a public Web
// Trigger to the key-gated path and returned 401). Mirrors the real driver type.
func TestWebTriggerConfig_ParsesRawJSONBData(t *testing.T) {
	RegisterTestingT(t)

	// Plain []byte — the type lib/pq yields for a jsonb column scanned into an
	// interface{}. NOT json.RawMessage: that implements json.Marshaler and would
	// round-trip cleanly, hiding the very bug this guards against.
	raw := []byte(`{
		"nodes": [
			{"data": {"label": "trigger/web", "config": {"inputs": [
				{"name": "methods", "value": "POST"},
				{"name": "auth_mode", "value": "public"}
			]}}}
		]
	}`)
	mock := &webTrigCfgMock{rev: &api.Revision{Data: raw}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := webTrigCfgRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	body := w.Body.String()
	Expect(body).To(ContainSubstring(`"found":true`))
	Expect(body).To(ContainSubstring(`"auth_mode":"public"`))
}

// The config projects the flow's "web" trigger id so Launch can invoke that
// trigger directly (starting from the Web Trigger node).
func TestWebTriggerConfig_ResolvesWebTriggerID(t *testing.T) {
	RegisterTestingT(t)

	revData := map[string]interface{}{
		"nodes": []map[string]interface{}{
			{"data": map[string]interface{}{
				"label":  "trigger/web",
				"config": map[string]interface{}{"inputs": []map[string]interface{}{{"name": "methods", "value": "POST"}}},
			}},
		},
	}
	mock := &webTrigCfgMock{
		rev: &api.Revision{Data: revData},
		triggers: []*api.Trigger{
			{ID: "manual-1", TypeName: "manual"},
			{ID: "web-1", TypeName: "web"},
		},
	}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := webTrigCfgRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"trigger_id":"web-1"`))
}

func TestWebTriggerConfig_NotFoundWhenNoWebTrigger(t *testing.T) {
	RegisterTestingT(t)
	revData := map[string]interface{}{"nodes": []map[string]interface{}{
		{"data": map[string]interface{}{"label": "trigger/manual"}},
	}}
	mock := &webTrigCfgMock{rev: &api.Revision{Data: revData}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := webTrigCfgRequest(svc)
	Expect(w.Body.String()).To(ContainSubstring(`"found":false`))
}
