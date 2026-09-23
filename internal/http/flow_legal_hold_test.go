package http

// An organisation that has not provided the legal identity its Data
// Processing Agreement needs used to have its flow runs REFUSED at the
// door with a 403. Every trigger that fired during that window — a
// webhook, an inbound message, a form submission — was simply lost,
// and nobody found out until someone went looking for a result that
// never arrived.
//
// The rule still applies; what changed is where it bites. The execution
// is created and queued as normal, and the dispatch queue declines to
// claim it until the details are complete (see GetExecutionForRunnerID).
// Nothing is lost, and the queue drains by itself the moment an admin
// fills the form in.
//
// These tests pin the half of that which lives in the HTTP layer: the
// request must no longer be refused.

import (
	"net/http"
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func incompleteOrgTriggerMock() *recordingTriggerMock {
	mock := newRecordingTriggerMock()
	orgID := "org-incomplete"
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", Name: "Flow", OrganisationID: &orgID}
	mock.organisations = map[string]*api.Organisation{
		// Missing legal_name, city, postcode, country and a company
		// number its type requires.
		orgID: {ID: orgID, Name: "Beta", CompanyType: strptr("limited_company")},
	}
	return mock
}

func TestTriggerFlo_IncompleteLegalDetails_StillQueuesTheExecution(t *testing.T) {
	RegisterTestingT(t)

	mock := incompleteOrgTriggerMock()
	svc := &Service{persistence: mock, executionNotifier: NewExecutionNotifier()}
	rec := makeTriggerRequest(t, setupTriggerFloRouter(svc, false), "")

	// The whole point: the trigger is accepted and an execution exists.
	// A 403 here means an inbound webhook has been thrown away.
	Expect(rec.Code).ToNot(Equal(http.StatusForbidden))
	Expect(mock.gotCalls).To(Equal(1), "the execution must be created, not refused")
}

func TestTriggerFlo_CompleteLegalDetails_Unaffected(t *testing.T) {
	RegisterTestingT(t)

	mock := newRecordingTriggerMock()
	orgID := "org-complete"
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", Name: "Flow", OrganisationID: &orgID}
	mock.organisations = map[string]*api.Organisation{
		orgID: {
			ID: orgID, Name: "Acme", CompanyType: strptr("limited_company"),
			LegalName: strptr("Acme Ltd"), CompanyNumber: strptr("12345678"),
			City: strptr("Manchester"), Postcode: strptr("M1 1AA"), Country: strptr("United Kingdom"),
		},
	}

	svc := &Service{persistence: mock, executionNotifier: NewExecutionNotifier()}
	rec := makeTriggerRequest(t, setupTriggerFloRouter(svc, false), "")

	Expect(rec.Code).ToNot(Equal(http.StatusForbidden))
	Expect(mock.gotCalls).To(Equal(1))
}
