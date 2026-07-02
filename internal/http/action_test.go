package http

// Tests for getActions serve-time augmentation. The invariant pinned here:
// inputs named in dynamicOptionsMetadata gain a dynamic_options marker in
// the served JSON, everything else is untouched — the marker lives only in
// code, never in the actions table.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// actionsMockPersistence overrides just GetActions; the embedded
// mockPersistence panics on everything else, which is fine — the
// handler only calls GetActions.
type actionsMockPersistence struct {
	mockPersistence
	actions []*api.Action
}

func (m *actionsMockPersistence) GetActions() ([]*api.Action, error) {
	return m.actions, nil
}

func TestGetActions_InjectsDynamicOptions(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	inputs, _ := json.Marshal([]api.InputDefinition{
		{Name: "api_key", Type: "secret", Label: "API Key", Required: true},
		{Name: "model", Type: "string", Label: "Model", Options: []api.InputOption{{Name: "Static", Value: "static/model"}}},
	})
	mock := &actionsMockPersistence{actions: []*api.Action{
		{ID: "ai/openrouter", ActionType: "2", Inputs: inputs},
		{ID: "ai/groq", ActionType: "2", Inputs: inputs},
	}}

	r := gin.New()
	svc := &Service{persistence: mock}
	r.GET("/api/v1/action", svc.getActions)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action", nil)
	r.ServeHTTP(rec, req)
	g.Expect(rec.Code).To(Equal(http.StatusOK))

	var served map[string]struct {
		Inputs []api.InputDefinition `json:"inputs"`
	}
	g.Expect(json.Unmarshal(rec.Body.Bytes(), &served)).To(Succeed())

	// ai/openrouter model input gains the marker; its static options survive.
	orInputs := served["ai/openrouter"].Inputs
	g.Expect(orInputs).To(HaveLen(2))
	g.Expect(orInputs[0].DynamicOptions).To(BeNil(), "api_key must not gain a marker")
	g.Expect(orInputs[1].DynamicOptions).To(Not(BeNil()))
	g.Expect(orInputs[1].DynamicOptions.Endpoint).To(Equal("/api/v1/action/options/openrouter-models"))
	g.Expect(orInputs[1].Options).To(HaveLen(1), "static options must survive as fallback")

	// The same input name on a different action is untouched.
	groqInputs := served["ai/groq"].Inputs
	g.Expect(groqInputs[1].DynamicOptions).To(BeNil())
}
