package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// googleAdsBaseURL is the Google Ads API root used to discover which ad
// accounts a freshly-authorised login can reach. Overridable in tests.
//
// Pinned to v25 to match the executor's google_ads_common; a sunset version
// fails outright rather than degrading, so the two must move together.
var googleAdsBaseURL = "https://googleads.googleapis.com/v25"

// googleAdsCustomer is one ad account the connected login can reach.
type googleAdsCustomer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Manager  bool   `json:"manager"`
	Currency string `json:"currency,omitempty"`
	TimeZone string `json:"time_zone,omitempty"`
}

// fetchGoogleAdsCustomers discovers the ad accounts a newly-authorised Google
// Ads login can reach.
//
// This is Google's awkward equivalent of Xero's /connections, and it takes two
// calls rather than one. customers:listAccessibleCustomers is the only endpoint
// that needs no customer id, but it returns bare resource names and NO
// descriptive names — useless on its own for an account picker — so each id is
// followed up with a one-row GAQL query for something a person can recognise.
//
// An account that cannot be read (cancelled, suspended, or simply not visible
// to this login) is skipped rather than failing the whole capture: one dead
// account in an agency's list must not cost the operator every other one.
func fetchGoogleAdsCustomers(accessToken string) ([]googleAdsCustomer, error) {
	ids, err := listAccessibleGoogleAdsCustomers(accessToken)
	if err != nil {
		return nil, err
	}

	var customers []googleAdsCustomer
	for _, id := range ids {
		customer, err := describeGoogleAdsCustomer(accessToken, id)
		if err != nil {
			// Keep the id: knowing the account exists but could not be read is
			// more useful than dropping it silently.
			customers = append(customers, googleAdsCustomer{ID: id})
			continue
		}
		customers = append(customers, customer)
	}
	return customers, nil
}

func listAccessibleGoogleAdsCustomers(accessToken string) ([]string, error) {
	body, err := googleAdsCall(accessToken, "", http.MethodGet, "/customers:listAccessibleCustomers", nil)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("parse accessible customers: %w", err)
	}

	ids := make([]string, 0, len(decoded.ResourceNames))
	for _, name := range decoded.ResourceNames {
		if id := strings.TrimPrefix(name, "customers/"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

const googleAdsCustomerQuery = "SELECT customer.id, customer.descriptive_name, customer.manager, " +
	"customer.currency_code, customer.time_zone FROM customer LIMIT 1"

func describeGoogleAdsCustomer(accessToken, customerID string) (googleAdsCustomer, error) {
	// login-customer-id is set to the account being read: for a manager account
	// the request has to be made AS that manager, and omitting it is an
	// authorisation error rather than a not-found.
	payload, _ := json.Marshal(map[string]string{"query": googleAdsCustomerQuery})
	body, err := googleAdsCall(accessToken, customerID, http.MethodPost,
		"/customers/"+customerID+"/googleAds:search", payload)
	if err != nil {
		return googleAdsCustomer{}, err
	}

	var decoded struct {
		Results []struct {
			Customer struct {
				ID              string `json:"id"`
				DescriptiveName string `json:"descriptiveName"`
				Manager         bool   `json:"manager"`
				CurrencyCode    string `json:"currencyCode"`
				TimeZone        string `json:"timeZone"`
			} `json:"customer"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return googleAdsCustomer{}, fmt.Errorf("parse customer: %w", err)
	}
	if len(decoded.Results) == 0 {
		return googleAdsCustomer{}, fmt.Errorf("customer %s returned no rows", customerID)
	}

	row := decoded.Results[0].Customer
	id := row.ID
	if id == "" {
		id = customerID
	}
	return googleAdsCustomer{
		ID:       id,
		Name:     row.DescriptiveName,
		Manager:  row.Manager,
		Currency: row.CurrencyCode,
		TimeZone: row.TimeZone,
	}, nil
}

func googleAdsCall(accessToken, loginCustomerID, method, path string, payload []byte) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, googleAdsBaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if loginCustomerID != "" {
		req.Header.Set("login-customer-id", loginCustomerID)
	}
	// No developer-token header. Developer tokens were sunset on 9 September
	// 2026 — access level now attaches to the Cloud project owning the OAuth
	// client, and Google promises to start rejecting the header in a future
	// major version.

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google ads %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// googleAdsTenantMetadata turns the discovered accounts into the credential
// metadata a flow reads through ${credentials.X.<key>}.
//
// The two scalar keys are only filled when they are UNAMBIGUOUS. Guessing a
// default ad account for a login that can reach several would silently point
// every flow at the wrong advertiser, which is far worse than leaving the field
// for the author to choose — the full list is stored either way so a picker can
// offer it.
func googleAdsTenantMetadata(customers []googleAdsCustomer) map[string]interface{} {
	kv := map[string]interface{}{"customers": customers}

	var managers, advertisers []googleAdsCustomer
	for _, c := range customers {
		if c.Manager {
			managers = append(managers, c)
			continue
		}
		advertisers = append(advertisers, c)
	}

	// login_customer_id is the manager an agency acts through. With exactly one
	// manager there is nothing to choose between, so the actions' hidden input
	// can auto-fill and the operator never has to find the number.
	if len(managers) == 1 {
		kv["login_customer_id"] = managers[0].ID
		kv["login_customer_name"] = managers[0].Name
	}
	if len(advertisers) == 1 {
		kv["customer_id"] = advertisers[0].ID
		kv["customer_name"] = advertisers[0].Name
	}
	return kv
}
