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
	"flomation.app/automate/api/internal/config"
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

// When OCI stack hosting is not configured, an OCI action is served with the
// managed connect inputs stripped and the raw signing fields un-gated + required.
func TestStripOCIConnectInputs(t *testing.T) {
	g := NewWithT(t)

	gate := &api.InputVisibleWhen{Field: "auth_method", Values: []string{"keys"}}
	in := []api.InputDefinition{
		{Name: "auth_method", Type: "string"},
		{Name: "credential", Type: "credential", VisibleWhen: &api.InputVisibleWhen{Field: "auth_method", Values: []string{"", "connect"}}},
		{Name: "tenancy_ocid", Type: "string", VisibleWhen: gate},
		{Name: "user_ocid", Type: "string", VisibleWhen: gate},
		{Name: "region", Type: "string", VisibleWhen: gate},
		{Name: "fingerprint", Type: "string", VisibleWhen: gate},
		{Name: "private_key", Type: "secret", VisibleWhen: gate},
		{Name: "private_key_passphrase", Type: "secret", VisibleWhen: gate},
		{Name: "compartment_ocid", Type: "string", Required: true}, // always-visible, untouched
		{Name: "zone_name", Type: "string", Required: true},        // resource field, untouched
	}

	out := stripOCIConnectInputs(in)

	byName := map[string]api.InputDefinition{}
	for _, c := range out {
		byName[c.Name] = c
	}
	// managed-connect inputs are gone
	g.Expect(byName).ToNot(HaveKey("auth_method"))
	g.Expect(byName).ToNot(HaveKey("credential"))
	// the four originally-required signing fields: un-gated + required again
	for _, n := range []string{"tenancy_ocid", "user_ocid", "region", "fingerprint"} {
		g.Expect(byName[n].VisibleWhen).To(BeNil(), n)
		g.Expect(byName[n].Required).To(BeTrue(), n)
	}
	// the two secret fields: un-gated, but stay optional
	for _, n := range []string{"private_key", "private_key_passphrase"} {
		g.Expect(byName[n].VisibleWhen).To(BeNil(), n)
		g.Expect(byName[n].Required).To(BeFalse(), n)
	}
	// non-auth fields are left exactly as they were
	g.Expect(byName["compartment_ocid"].Required).To(BeTrue())
	g.Expect(byName["zone_name"].Required).To(BeTrue())
	g.Expect(out).To(HaveLen(8)) // 10 in, 2 removed
}

// End-to-end at the handler: getActions strips the OCI connect block from oracle/*
// actions ONLY when the server has no oci_hosting config, and leaves everyone else
// (and, when hosted, the OCI block itself) untouched.
func TestGetActions_StripsOCIConnectWhenUnhosted(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	gate := &api.InputVisibleWhen{Field: "auth_method", Values: []string{"keys"}}
	ociInputs, _ := json.Marshal([]api.InputDefinition{
		{Name: "auth_method", Type: "string", Options: []api.InputOption{{Name: "Connect Oracle Cloud", Value: "connect"}, {Name: "API signing key (advanced)", Value: "keys"}}},
		{Name: "credential", Type: "credential", VisibleWhen: &api.InputVisibleWhen{Field: "auth_method", Values: []string{"", "connect"}}},
		{Name: "tenancy_ocid", Type: "string", VisibleWhen: gate},
		{Name: "user_ocid", Type: "string", VisibleWhen: gate},
		{Name: "region", Type: "string", VisibleWhen: gate},
		{Name: "fingerprint", Type: "string", VisibleWhen: gate},
		{Name: "private_key", Type: "secret", VisibleWhen: gate},
		{Name: "private_key_passphrase", Type: "secret", VisibleWhen: gate},
		{Name: "compartment_ocid", Type: "string", Required: true},
	})
	// A non-oracle action that ALSO keys visibility off auth_method (e.g. AWS/Azure
	// connect) must be left untouched — the strip is scoped to oracle/* ids only.
	nonOCI, _ := json.Marshal([]api.InputDefinition{
		{Name: "auth_method", Type: "string"},
		{Name: "credential", Type: "credential", VisibleWhen: &api.InputVisibleWhen{Field: "auth_method", Values: []string{"", "connect"}}},
	})
	newMock := func() *actionsMockPersistence {
		return &actionsMockPersistence{actions: []*api.Action{
			{ID: "oracle/dns/zone_list", ActionType: "2", Inputs: append([]byte(nil), ociInputs...)},
			{ID: "aws/ec2/instance_list", ActionType: "2", Inputs: append([]byte(nil), nonOCI...)},
		}}
	}
	type served map[string]struct {
		Inputs []api.InputDefinition `json:"inputs"`
	}
	serve := func(svc *Service) served {
		r := gin.New()
		r.GET("/api/v1/action", svc.getActions)
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/action", nil)
		r.ServeHTTP(rec, req)
		g.Expect(rec.Code).To(Equal(http.StatusOK))
		var s served
		g.Expect(json.Unmarshal(rec.Body.Bytes(), &s)).To(Succeed())
		return s
	}
	names := func(ins []api.InputDefinition) []string {
		var n []string
		for _, c := range ins {
			n = append(n, c.Name)
		}
		return n
	}

	// --- unhosted (nil config => ociHostConfigured()==false): OCI connect block gone,
	// raw fields un-gated + required; the non-oracle action is untouched.
	unhosted := serve(&Service{persistence: newMock()})
	oci := unhosted["oracle/dns/zone_list"].Inputs
	g.Expect(names(oci)).ToNot(ContainElement("auth_method"))
	g.Expect(names(oci)).ToNot(ContainElement("credential"))
	g.Expect(oci[0].Name).To(Equal("tenancy_ocid"))
	g.Expect(oci[0].VisibleWhen).To(BeNil())
	g.Expect(oci[0].Required).To(BeTrue())
	g.Expect(names(unhosted["aws/ec2/instance_list"].Inputs)).To(ContainElement("credential"), "non-oracle action must be untouched")

	// --- hosted: the OCI connect block is served intact.
	hosted := serve(&Service{persistence: newMock(), config: &config.Config{
		OCIHosting: &config.OCIHostingConfig{Bucket: "b", Tenancy: "t", PrivateKey: "k"},
	}})
	oci2 := hosted["oracle/dns/zone_list"].Inputs
	g.Expect(names(oci2)[:2]).To(Equal([]string{"auth_method", "credential"}))
	g.Expect(oci2[2].Name).To(Equal("tenancy_ocid"))
	g.Expect(oci2[2].VisibleWhen).ToNot(BeNil())
}
