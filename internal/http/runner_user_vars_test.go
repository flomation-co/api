package http

import (
	"encoding/json"
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func TestEnrichDataWithUserVariables_AddsProfileToExecutionData(t *testing.T) {
	RegisterTestingT(t)

	mock := newMockPersistence()
	mr := "Mr"
	andy := "Andy"
	esser := "Esser"
	postcode := "SW1A 2AA"
	city := "London"
	mock.users["user-1"] = &api.User{
		ID:         "user-1",
		Name:       "andy@example.com",
		Salutation: &mr,
		FirstName:  &andy,
		LastName:   &esser,
		Postcode:   &postcode,
		City:       &city,
	}

	// Simulate an execution that already has user_id set (the inbound
	// agent pipeline or enrichDataWithAuthorIdentities populated it).
	exec := &api.Execution{
		ID:      "exec-1",
		OwnerID: "user-1",
		Data:    json.RawMessage(`{"user_id":"user-1"}`),
	}

	enrichDataWithUserVariables(mock, exec)

	var data map[string]interface{}
	Expect(json.Unmarshal(exec.Data, &data)).To(Succeed())
	vars, ok := data["user_variables"].(map[string]interface{})
	Expect(ok).To(BeTrue(), "user_variables map should be present")
	Expect(vars["first_name"]).To(Equal("Andy"))
	Expect(vars["last_name"]).To(Equal("Esser"))
	Expect(vars["salutation"]).To(Equal("Mr"))
	Expect(vars["full_name"]).To(Equal("Mr Andy Esser"))
	Expect(vars["full_address"]).To(Equal("London\nSW1A 2AA"))
	// Unset fields surface as empty strings, not missing keys
	Expect(vars["job_title"]).To(Equal(""))
	Expect(vars["country"]).To(Equal(""))
}

func TestEnrichDataWithUserVariables_NoOpWhenUserIDMissing(t *testing.T) {
	RegisterTestingT(t)

	mock := newMockPersistence()
	exec := &api.Execution{
		ID:   "exec-1",
		Data: json.RawMessage(`{"other_field":"x"}`),
	}

	enrichDataWithUserVariables(mock, exec)

	var data map[string]interface{}
	Expect(json.Unmarshal(exec.Data, &data)).To(Succeed())
	_, hasVars := data["user_variables"]
	Expect(hasVars).To(BeFalse(), "should not add user_variables without a user_id")
}

func TestEnrichDataWithUserVariables_NoOpWhenUserUnknown(t *testing.T) {
	RegisterTestingT(t)

	mock := newMockPersistence()
	// users map is empty — GetUserByID returns (nil, nil)
	exec := &api.Execution{
		Data: json.RawMessage(`{"user_id":"nope"}`),
	}

	enrichDataWithUserVariables(mock, exec)

	var data map[string]interface{}
	Expect(json.Unmarshal(exec.Data, &data)).To(Succeed())
	_, hasVars := data["user_variables"]
	Expect(hasVars).To(BeFalse())
}
