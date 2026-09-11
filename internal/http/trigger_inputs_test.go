package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"
)

func TestValidateTriggerInputs_MissingRequired(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{
		{Name: "name", Type: "string", Required: true},
		{Name: "note", Type: "string", Required: false},
	}

	// Absent entirely.
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{})).To(Equal([]string{"name"}))
	// Present but whitespace-only counts as empty.
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"name": "   "})).To(Equal([]string{"name"}))
	// A non-required empty field is fine.
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"name": "Ada", "note": ""})).To(BeEmpty())
}

func TestValidateTriggerInputs_IntegerMismatch(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{{Name: "count", Type: "integer", Required: true}}

	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"count": "not-a-number"})).To(Equal([]string{"count"}))
	// Numeric string is accepted.
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"count": "42"})).To(BeEmpty())
	// Native JSON number is accepted.
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"count": float64(7)})).To(BeEmpty())
}

func TestValidateTriggerInputs_BooleanMismatch(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{{Name: "flag", Type: "boolean", Required: true}}

	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"flag": "yes"})).To(Equal([]string{"flag"}))
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"flag": "true"})).To(BeEmpty())
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"flag": false})).To(BeEmpty())
}

func TestValidateTriggerInputs_DateMismatch(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{{Name: "when", Type: "date", Required: true}}

	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"when": "31/12/2026"})).To(Equal([]string{"when"}))
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"when": "2026-12-31"})).To(BeEmpty())
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"when": "2026-12-31T09:30:00Z"})).To(BeEmpty())
}

func TestValidateTriggerInputs_DropdownNotInOptions(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{{
		Name:     "colour",
		Type:     "dropdown",
		Required: true,
		Options: []TriggerInputOption{
			{Label: "Red", Value: "red"},
			{Label: "Green", Value: "green"},
		},
	}}

	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"colour": "blue"})).To(Equal([]string{"colour"}))
	Expect(ValidateTriggerInputs(schema, map[string]interface{}{"colour": "green"})).To(BeEmpty())
}

func TestValidateTriggerInputs_AllGood(t *testing.T) {
	RegisterTestingT(t)

	schema := []TriggerInput{
		{Name: "name", Type: "string", Required: true},
		{Name: "count", Type: "integer", Required: true},
		{Name: "flag", Type: "boolean", Required: false},
		{Name: "when", Type: "date", Required: false},
	}

	data := map[string]interface{}{
		"name":  "Grace",
		"count": "3",
		"flag":  "false",
		"when":  "2026-07-12",
	}

	Expect(ValidateTriggerInputs(schema, data)).To(BeEmpty())
}

func TestManualTriggerRegistrationData_Shape(t *testing.T) {
	RegisterTestingT(t)

	ti := []map[string]interface{}{
		{"name": "name", "type": "string", "required": true},
	}
	got := manualTriggerRegistrationData("node-9", ti, "${secrets.RUN_TOKEN}")

	Expect(got["__node_id"]).To(Equal("node-9"))
	Expect(got["run_token"]).To(Equal("${secrets.RUN_TOKEN}"))
	Expect(got["trigger_inputs"]).To(Equal(ti))

	// A nil schema is normalised to an empty (never nil) slice so the
	// registered payload always has a well-formed trigger_inputs array.
	empty := manualTriggerRegistrationData("node-1", nil, "")
	Expect(empty["trigger_inputs"]).To(Equal([]map[string]interface{}{}))
}

func TestExtractManualTriggerInputs(t *testing.T) {
	RegisterTestingT(t)

	data := manualRevisionData([]map[string]interface{}{
		{"name": "name", "label": "Name", "type": "string", "required": true},
	})

	schema, ok := extractManualTriggerInputs(data)
	Expect(ok).To(BeTrue())
	Expect(schema).To(HaveLen(1))
	Expect(schema[0].Name).To(Equal("name"))
	Expect(schema[0].Required).To(BeTrue())

	// A revision with no manual node yields ok == false.
	noManual := []byte(`{"nodes":[{"type":"trigger/schedule","data":{"label":"trigger/schedule","config":{}}}]}`)
	_, ok = extractManualTriggerInputs(noManual)
	Expect(ok).To(BeFalse())
}

// --- triggerFlo handler tests ---------------------------------------------

// manualRevisionData builds a flow revision blob whose only node is a
// manual trigger declaring the given trigger_inputs.
func manualRevisionData(inputs []map[string]interface{}) []byte {
	doc := map[string]interface{}{
		"nodes": []map[string]interface{}{
			{
				"id":   "manual-node",
				"type": "trigger/manual",
				"data": map[string]interface{}{
					"label": "trigger/manual",
					"config": map[string]interface{}{
						"type":           1,
						"trigger_inputs": inputs,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(doc)
	return b
}

func setupTriggerRouter(svc *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/flo/:FloID/trigger/:TriggerID/execute", svc.triggerFlo)
	return r
}

func TestTriggerFlo_MissingRequiredInput_Returns400_NoExecution(t *testing.T) {
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.latestRevision = &api.Revision{
		Data: manualRevisionData([]map[string]interface{}{
			{"name": "name", "label": "Name", "type": "string", "required": true},
		}),
	}

	svc := &Service{persistence: mock, executionNotifier: NewExecutionNotifier()}
	r := setupTriggerRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/flo/flo-1/trigger/default/execute", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusBadRequest))

	var resp struct {
		Error  string   `json:"error"`
		Fields []string `json:"fields"`
	}
	Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp.Error).To(Equal("missing or invalid inputs"))
	Expect(resp.Fields).To(Equal([]string{"name"}))

	// No execution must be created when validation fails.
	Expect(mock.triggerExecCalls).To(Equal(0))
}

func TestTriggerFlo_ValidInput_CreatesExecution(t *testing.T) {
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.latestRevision = &api.Revision{
		Data: manualRevisionData([]map[string]interface{}{
			{"name": "name", "label": "Name", "type": "string", "required": true},
		}),
	}

	svc := &Service{persistence: mock, executionNotifier: NewExecutionNotifier()}
	r := setupTriggerRouter(svc)

	body := `{"name":"Ada"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/flo/flo-1/trigger/default/execute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusCreated))
	Expect(mock.triggerExecCalls).To(Equal(1))
}

func TestFormTriggerData_ReStampsNodeID(t *testing.T) {
	RegisterTestingT(t)
	// The form_definition becomes the root trigger data, and __node_id must
	// survive so the executor injects a submission into THIS form trigger node.
	in := map[string]interface{}{"form_definition": `{"title":"T","pages":[]}`}
	out := formTriggerData(in, "node-abc")
	Expect(out["__node_id"]).To(Equal("node-abc"))
	Expect(out["title"]).To(Equal("T"))
	Expect(out).NotTo(HaveKey("form_definition")) // replaced by the parsed definition

	// No form_definition → data unchanged.
	plain := map[string]interface{}{"foo": "bar"}
	Expect(formTriggerData(plain, "node-abc")).To(Equal(plain))
}
