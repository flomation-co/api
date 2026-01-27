package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"flomation.app/automate/api/internal/config"
)

type Account struct {
	ID        string     `json:"id"`
	Username  string     `json:"username"`
	CreatedOn *time.Time `json:"created_on" `
	Locked    bool       `json:"locked"`
	LastLogin *time.Time `json:"last_login" `
	Type      int64      `json:"type"`
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
