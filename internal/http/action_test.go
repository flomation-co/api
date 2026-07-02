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

// TestGetActions_InjectsParameterisedDynamicOptions pins the params leg of
// the marker: ai/ollama's model input declares the sibling inputs whose
// values the editor must forward to the resolver as query parameters.
func TestGetActions_InjectsParameterisedDynamicOptions(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	inputs, _ := json.Marshal([]api.InputDefinition{
		{Name: "endpoint", Type: "string", Label: "Ollama Server URL", Required: true},
		{Name: "model", Type: "string", Label: "Model", Options: []api.InputOption{{Name: "Llama 3.2", Value: "llama3.2"}}},
	})
	mock := &actionsMockPersistence{actions: []*api.Action{
		{ID: "ai/ollama", ActionType: "2", Inputs: inputs},
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

	model := served["ai/ollama"].Inputs[1]
	g.Expect(model.DynamicOptions).To(Not(BeNil()))
	g.Expect(model.DynamicOptions.Endpoint).To(Equal("/api/v1/action/options/ollama-models"))
	g.Expect(model.DynamicOptions.Params).To(Equal([]string{"endpoint", "api_key"}))
	g.Expect(model.Options).To(HaveLen(1), "static options must survive as fallback")
}

// TestGetCategoryForShopify pins the E-Commerce > Shopify wiring: a 3-segment
// ecommerce/shopify/* action resolves to the E-Commerce top-level category
// with the Shopify sub-group (from subCategoryMetadata, not auto-titled).
func TestGetCategoryForShopify(t *testing.T) {
	g := NewWithT(t)
	cat := getCategoryForAction("ecommerce/shopify/order_create")
	g.Expect(cat).To(Not(BeNil()))
	g.Expect(cat.Key).To(Equal("ecommerce"))
	g.Expect(cat.Name).To(Equal("E-Commerce"))
	g.Expect(cat.Icon).To(Equal("cart-shopping"))
	g.Expect(cat.SubKey).To(Equal("ecommerce/shopify"))
	g.Expect(cat.SubName).To(Equal("Shopify"))
	g.Expect(cat.SubIcon).To(Equal("shopify"))
}
