package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"flomation.app/automate/api/internal/config"
)

type Account struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName *string    `json:"display_name"`
	CreatedOn   *time.Time `json:"created_on"`
	Locked      bool       `json:"locked"`
	LastLogin   *time.Time `json:"last_login"`
	Type        int64      `json:"type"`

	// Marketing consent as recorded at sign-up. Sentinel owns the point of
	// collection, so this is the decision the user actually gave; we seed our
	// own copy from it rather than asking again. MarketingConsentAt is nil for
	// accounts created before the sign-up question existed, and for SSO
	// sign-ups, where there is no form of ours to put the question on.
	MarketingOptIn          bool       `json:"marketing_opt_in"`
	MarketingConsentAt      *time.Time `json:"marketing_consent_at"`
	MarketingConsentSource  *string    `json:"marketing_consent_source"`
	MarketingConsentVersion *string    `json:"marketing_consent_version"`
}

type Connector struct {
	config *config.Config
}

func NewConnector(config *config.Config) *Connector {
	return &Connector{
		config: config,
	}
}

func (c *Connector) GetAccount(token string) (*Account, error) {
	client := http.Client{
		Timeout: time.Second * 10,
	}

	url := fmt.Sprintf("%v/api/account", c.config.Security.IdentityService)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Authorization", "Bearer "+token)

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("url: %v invalid status: %v", url, response.Status)
	}

	if response.Body == nil {
		return nil, nil
	}

	defer func() {
		_ = response.Body.Close()
	}()

	b, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	var account Account
	if err := json.Unmarshal(b, &account); err != nil {
		return nil, err
	}

	return &account, nil
}

func (c *Connector) UpdateDisplayName(token string, displayName string) error {
	client := http.Client{
		Timeout: time.Second * 10,
	}

	url := fmt.Sprintf("%v/api/user", c.config.Security.IdentityService)

	body, err := json.Marshal(map[string]string{
		"display_name": displayName,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("unable to update display name: %v", response.Status)
	}

	return nil
}
