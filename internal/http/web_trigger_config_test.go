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
	rev *api.Revision
}

func (m *webTrigCfgMock) GetLatestRevisionByFloID(id string) (*api.Revision, error) {
	return m.rev, nil
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
						{"name": "keep_history", "value": true},
						{"name": "message_field", "value": "prompt"},
						{"name": "auth", "value": "publishable"},
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
	Expect(body).To(ContainSubstring(`"auth":"publishable"`))
	Expect(body).To(ContainSubstring(`"keep_history":true`))
	Expect(body).To(ContainSubstring(`"message_field":"prompt"`))
	Expect(body).To(ContainSubstring(`"GET"`))
	Expect(body).To(ContainSubstring(`"POST"`))
	Expect(body).To(ContainSubstring(`"id":"path"`))
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
