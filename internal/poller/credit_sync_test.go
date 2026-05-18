package poller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	api "flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

type mockCreditSyncPersistence struct {
	deductions []*api.CreditDeduction
	mu         sync.Mutex
	synced     []string
}

func (m *mockCreditSyncPersistence) GetUnsyncedDeductions() ([]*api.CreditDeduction, error) {
	return m.deductions, nil
}

func (m *mockCreditSyncPersistence) MarkDeductionSynced(id string, amountPence int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synced = append(m.synced, id)
	return nil
}

func TestCreditSyncPoller_PollSyncsDeductions(t *testing.T) {
	RegisterTestingT(t)

	orgID := "org-1"
	mock := &mockCreditSyncPersistence{
		deductions: []*api.CreditDeduction{
			{
				ID:             "ded-1",
				OwnerID:        "owner-1",
				OrganisationID: &orgID,
				ExecutionID:    "exec-1",
				DurationMs:     126000,
			},
			{
				ID:          "ded-2",
				OwnerID:     "owner-2",
				ExecutionID: "exec-2",
				DurationMs:  30000,
			},
		},
	}

	var receivedBodies []creditDeductPayload
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body creditDeductPayload
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		receivedBodies = append(receivedBodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"balance_pence": 100}`))
	}))
	defer server.Close()

	cp := &CreditSyncPoller{
		persistence: mock,
		billingURL:  server.URL,
		client:      server.Client(),
	}

	cp.poll()

	Expect(receivedBodies).To(HaveLen(2))
	Expect(receivedBodies[0].OwnerID).To(Equal("owner-1"))
	Expect(receivedBodies[0].ExecutionID).To(Equal("exec-1"))
	Expect(receivedBodies[0].DurationMs).To(Equal(int64(126000)))
	Expect(receivedBodies[0].OrganisationID).NotTo(BeNil())
	Expect(*receivedBodies[0].OrganisationID).To(Equal("org-1"))
	Expect(receivedBodies[1].OwnerID).To(Equal("owner-2"))
	Expect(receivedBodies[1].OrganisationID).To(BeNil())

	mock.mu.Lock()
	Expect(mock.synced).To(Equal([]string{"ded-1", "ded-2"}))
	mock.mu.Unlock()
}

func TestCreditSyncPoller_PollSkipsOnBillingError(t *testing.T) {
	RegisterTestingT(t)

	mock := &mockCreditSyncPersistence{
		deductions: []*api.CreditDeduction{
			{
				ID:          "ded-1",
				OwnerID:     "owner-1",
				ExecutionID: "exec-1",
				DurationMs:  30000,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cp := &CreditSyncPoller{
		persistence: mock,
		billingURL:  server.URL,
		client:      server.Client(),
	}

	cp.poll()

	mock.mu.Lock()
	Expect(mock.synced).To(BeEmpty())
	mock.mu.Unlock()
}

func TestCreditSyncPoller_PollNoOpOnEmptyList(t *testing.T) {
	RegisterTestingT(t)

	mock := &mockCreditSyncPersistence{
		deductions: []*api.CreditDeduction{},
	}

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cp := &CreditSyncPoller{
		persistence: mock,
		billingURL:  server.URL,
		client:      server.Client(),
	}

	cp.poll()

	Expect(called).To(BeFalse())
}
