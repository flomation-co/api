package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// listAccessibleCustomers returns bare resource names and no descriptive names,
// so the capture needs a second call per account to produce anything a person
// can recognise in a picker.
func TestFetchGoogleAdsCustomers_ResolvesNamesForEachAccessibleAccount(t *testing.T) {
	RegisterTestingT(t)

	var logins []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/customers:listAccessibleCustomers" {
			Expect(r.Method).To(Equal(http.MethodGet))
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer tok"))
			// Developer tokens were sunset on 9 September 2026.
			Expect(r.Header.Get("developer-token")).To(Equal(""))
			fmt.Fprint(w, `{"resourceNames":["customers/1111111111","customers/2222222222"]}`)
			return
		}

		// Reading an account has to be done AS that account, or a manager
		// yields an authorisation error rather than a not-found.
		logins = append(logins, r.Header.Get("login-customer-id"))
		id := strings.Split(r.URL.Path, "/")[2]
		manager := id == "1111111111"
		fmt.Fprintf(w, `{"results":[{"customer":{"id":"%s","descriptiveName":"Account %s","manager":%t,"currencyCode":"GBP","timeZone":"Europe/London"}}]}`, id, id, manager)
	}))
	defer server.Close()

	original := googleAdsBaseURL
	googleAdsBaseURL = server.URL
	defer func() { googleAdsBaseURL = original }()

	customers, err := fetchGoogleAdsCustomers("tok")
	Expect(err).To(BeNil())
	Expect(customers).To(HaveLen(2))

	Expect(customers[0].ID).To(Equal("1111111111"))
	Expect(customers[0].Name).To(Equal("Account 1111111111"))
	Expect(customers[0].Manager).To(BeTrue())
	Expect(customers[0].Currency).To(Equal("GBP"))

	Expect(logins).To(Equal([]string{"1111111111", "2222222222"}))
}

// A cancelled or suspended account in an agency's list must not cost the
// operator every other account in it.
func TestFetchGoogleAdsCustomers_KeepsGoingPastAnUnreadableAccount(t *testing.T) {
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/customers:listAccessibleCustomers" {
			fmt.Fprint(w, `{"resourceNames":["customers/1111111111","customers/2222222222"]}`)
			return
		}
		if strings.Contains(r.URL.Path, "1111111111") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"message":"The caller does not have permission"}}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"customer":{"id":"2222222222","descriptiveName":"Live Account","manager":false}}]}`)
	}))
	defer server.Close()

	original := googleAdsBaseURL
	googleAdsBaseURL = server.URL
	defer func() { googleAdsBaseURL = original }()

	customers, err := fetchGoogleAdsCustomers("tok")
	Expect(err).To(BeNil())
	Expect(customers).To(HaveLen(2))

	// The unreadable one keeps its id — knowing it exists is more useful than
	// dropping it silently.
	Expect(customers[0].ID).To(Equal("1111111111"))
	Expect(customers[0].Name).To(Equal(""))
	Expect(customers[1].Name).To(Equal("Live Account"))
}

// If listAccessibleCustomers itself fails there is nothing to capture, and the
// caller logs and moves on rather than failing the OAuth callback.
func TestFetchGoogleAdsCustomers_ErrorsWhenTheListingFails(t *testing.T) {
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Invalid credentials"}}`)
	}))
	defer server.Close()

	original := googleAdsBaseURL
	googleAdsBaseURL = server.URL
	defer func() { googleAdsBaseURL = original }()

	_, err := fetchGoogleAdsCustomers("tok")
	Expect(err).ToNot(BeNil())
	Expect(err.Error()).To(ContainSubstring("401"))
}

// The scalar keys exist so the actions can auto-fill. Filling them from an
// ambiguous list would silently point every flow at the wrong advertiser.
func TestGoogleAdsTenantMetadata_OnlyFillsScalarsWhenUnambiguous(t *testing.T) {
	RegisterTestingT(t)

	// One manager, one advertiser: both are unambiguous.
	kv := googleAdsTenantMetadata([]googleAdsCustomer{
		{ID: "1111111111", Name: "Agency MCC", Manager: true},
		{ID: "2222222222", Name: "Client Ltd"},
	})
	Expect(kv["login_customer_id"]).To(Equal("1111111111"))
	Expect(kv["login_customer_name"]).To(Equal("Agency MCC"))
	Expect(kv["customer_id"]).To(Equal("2222222222"))
	Expect(kv["customer_name"]).To(Equal("Client Ltd"))

	// Two advertisers: which one a flow means is the author's choice.
	kv = googleAdsTenantMetadata([]googleAdsCustomer{
		{ID: "1111111111", Name: "Agency MCC", Manager: true},
		{ID: "2222222222", Name: "Client One"},
		{ID: "3333333333", Name: "Client Two"},
	})
	Expect(kv["login_customer_id"]).To(Equal("1111111111"))
	Expect(kv).ToNot(HaveKey("customer_id"))

	// Two managers: likewise.
	kv = googleAdsTenantMetadata([]googleAdsCustomer{
		{ID: "1111111111", Manager: true},
		{ID: "4444444444", Manager: true},
		{ID: "2222222222"},
	})
	Expect(kv).ToNot(HaveKey("login_customer_id"))
	Expect(kv["customer_id"]).To(Equal("2222222222"))
}

// A direct advertiser with no manager above them must NOT get a
// login_customer_id: sending that header when the token is not a manager is an
// authorisation error, not a harmless extra.
func TestGoogleAdsTenantMetadata_NoManagerMeansNoLoginCustomerId(t *testing.T) {
	RegisterTestingT(t)

	kv := googleAdsTenantMetadata([]googleAdsCustomer{{ID: "2222222222", Name: "Just Us Ltd"}})

	Expect(kv).ToNot(HaveKey("login_customer_id"))
	Expect(kv["customer_id"]).To(Equal("2222222222"))
}

// The full list always survives so a picker can offer every account, whatever
// the scalars resolved to.
func TestGoogleAdsTenantMetadata_AlwaysKeepsTheFullList(t *testing.T) {
	RegisterTestingT(t)

	customers := []googleAdsCustomer{
		{ID: "1111111111", Name: "A", Manager: true},
		{ID: "2222222222", Name: "B"},
		{ID: "3333333333", Name: "C"},
	}
	kv := googleAdsTenantMetadata(customers)

	// It has to survive a JSON round trip: this lands in a JSONB metadata blob.
	encoded, err := json.Marshal(kv["customers"])
	Expect(err).To(BeNil())

	var decoded []googleAdsCustomer
	Expect(json.Unmarshal(encoded, &decoded)).To(BeNil())
	Expect(decoded).To(Equal(customers))
}
