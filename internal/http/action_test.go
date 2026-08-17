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
		// A negative control: an action absent from dynamicOptionsMetadata
		// must not gain a marker on its "model" input. (Most ai/* providers
		// now DO have a live-model marker, so this uses a synthetic id.)
		{ID: "test/untouched", ActionType: "2", Inputs: inputs},
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

	// The same input name on an action that has no marker is untouched.
	untouchedInputs := served["test/untouched"].Inputs
	g.Expect(untouchedInputs[1].DynamicOptions).To(BeNil())
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

// ukgov/<agency>/* actions resolve to the UK Government top-level category with
// the agency sub-group (from subCategoryMetadata, not auto-titled).
func TestGetCategoryForUKGov(t *testing.T) {
	g := NewWithT(t)

	cat := getCategoryForAction("ukgov/companieshouse/get_company")
	g.Expect(cat).To(Not(BeNil()))
	g.Expect(cat.Key).To(Equal("ukgov"))
	g.Expect(cat.Name).To(Equal("UK Government"))
	g.Expect(cat.Icon).To(Equal("landmark"))
	g.Expect(cat.SubKey).To(Equal("ukgov/companieshouse"))
	g.Expect(cat.SubName).To(Equal("Companies House"))
	g.Expect(cat.SubIcon).To(Equal("briefcase"))

	// A different agency under the same top-level category.
	dvla := getCategoryForAction("ukgov/dvla/vehicle_enquiry")
	g.Expect(dvla).To(Not(BeNil()))
	g.Expect(dvla.Key).To(Equal("ukgov"))
	g.Expect(dvla.SubName).To(Equal("DVLA"))

	// A v2 agency (Parliament) resolves under the same top-level category.
	parl := getCategoryForAction("ukgov/parliament/search_members")
	g.Expect(parl).To(Not(BeNil()))
	g.Expect(parl.Key).To(Equal("ukgov"))
	g.Expect(parl.SubKey).To(Equal("ukgov/parliament"))
	g.Expect(parl.SubName).To(Equal("UK Parliament"))
	g.Expect(parl.SubIcon).To(Equal("landmark"))
}

// TestGetActions_TolerantOfNonStringInputDefaults — an input's default takes the
// shape of its own type, so a boolean input defaults to a real `true`.
//
// InputDefinition.Value used to be a string, and getActions aborts with 400 on
// ANY unmarshal error, so the first action to ship a boolean default did not
// degrade that one field — it emptied the ENTIRE palette. The editor sat on
// "Loading..." with "Unable to fetch actions", and nothing in the failure
// pointed at a single checkbox in an unrelated integration.
//
// The blast radius is the point: one malformed-looking value must never be able
// to take down the whole action list.
func TestGetActions_TolerantOfNonStringInputDefaults(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	// Raw JSON rather than a marshalled struct, so this reproduces exactly what
	// the actions table holds after a manifest migration.
	inputs := []byte(`[
		{"name":"reveal_personal_emails","type":"boolean","label":"Reveal Personal Emails","value":true},
		{"name":"force","type":"boolean","label":"Force Delete","value":false},
		{"name":"format","type":"string","label":"Format","value":"png"},
		{"name":"per_page","type":"integer","label":"Per Page","value":25},
		{"name":"api_key","type":"secret","label":"API Key","value":null}
	]`)

	mock := &actionsMockPersistence{actions: []*api.Action{
		{ID: "crm/apollo/enrichment/people_match", ActionType: "2", Inputs: inputs},
		// A second action proves the failure was never scoped to one entry.
		{ID: "ai/openrouter", ActionType: "2", Inputs: inputs},
	}}

	r := gin.New()
	svc := &Service{persistence: mock}
	r.GET("/api/v1/action", svc.getActions)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/action", nil)
	r.ServeHTTP(rec, req)
	g.Expect(rec.Code).To(Equal(http.StatusOK), "a boolean default must not 400 the whole action list")

	var served map[string]struct {
		Inputs []api.InputDefinition `json:"inputs"`
	}
	g.Expect(json.Unmarshal(rec.Body.Bytes(), &served)).To(Succeed())
	g.Expect(served).To(HaveLen(2), "every action must survive, not just the first")

	got := served["crm/apollo/enrichment/people_match"].Inputs
	g.Expect(got).To(HaveLen(5))

	// Defaults must reach the editor with their type intact: a boolean has to
	// arrive as a real bool, because the editor renders a checkbox from
	// `typeof value === "boolean"` and a string "true" would render UNTICKED —
	// the exact mismatch the default was added to remove.
	g.Expect(got[0].Value).To(BeAssignableToTypeOf(true))
	g.Expect(got[0].Value).To(Equal(true))
	g.Expect(got[1].Value).To(Equal(false))
	g.Expect(got[2].Value).To(Equal("png"))
	g.Expect(got[3].Value).To(BeNumerically("==", 25))
	g.Expect(got[4].Value).To(BeNil())
}

// TestGetCategoryForMetaAds pins the three-level Marketing > Meta Ads > <group>
// wiring for 4-segment marketing/meta_ads/<group>/<action> ids.
//
// The executor's category.go files are NOT what the editor reads — the palette
// is served from these maps, so a missing entry here leaves the actions
// auto-titled or ungrouped even though the executor side looks correct.
func TestGetCategoryForMetaAds(t *testing.T) {
	g := NewWithT(t)

	cat := getCategoryForAction("marketing/meta_ads/campaigns/campaign_create")
	g.Expect(cat).To(Not(BeNil()))
	g.Expect(cat.Key).To(Equal("marketing"))
	g.Expect(cat.Name).To(Equal("Marketing"))
	g.Expect(cat.SubKey).To(Equal("marketing/meta_ads"))
	g.Expect(cat.SubName).To(Equal("Meta Ads"))
	g.Expect(cat.SubSubKey).To(Equal("marketing/meta_ads/campaigns"))
	g.Expect(cat.SubSubName).To(Equal("Campaigns"))

	// Every group must resolve — a missing one silently degrades that group
	// rather than failing loudly.
	for group, want := range map[string]string{
		"accounts": "Accounts", "campaigns": "Campaigns",
		"adsets": "Ad Sets", "ads": "Ads", "insights": "Insights",
	} {
		c := getCategoryForAction("marketing/meta_ads/" + group + "/whatever")
		g.Expect(c).To(Not(BeNil()), group)
		g.Expect(c.SubSubName).To(Equal(want), group)
	}

	// The existing 3-segment Marketing action must be unaffected.
	sg := getCategoryForAction("marketing/sendgrid/mail_send")
	g.Expect(sg.SubName).To(Equal("SendGrid"))
	g.Expect(sg.SubSubName).To(Equal(""))
}
