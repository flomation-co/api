// Package sentinel is the API's client for Sentinel's internal SSO admin API.
// The API is the authenticated org-admin front door; it validates the caller
// then forwards connection/domain CRUD here, presenting the shared service
// token. (mTLS is a later hardening; the target routes are internal-only.)
package sentinel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"flomation.app/automate/api/internal/config"
)

type Connector struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewConnector(cfg *config.Config) *Connector {
	return &Connector{
		baseURL: strings.TrimRight(cfg.Security.IdentityService, "/"),
		token:   cfg.Security.ServiceToken,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Connector) do(method, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sentinel sso admin %s %s: %d %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ── Connections ──────────────────────────────────────────────────────

func (c *Connector) ListConnections(orgID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodGet, "/internal/sso/connection?organisation_id="+orgID, nil, &out)
	return out, err
}

// CreateConnection forwards the connection body (organisation_id already stamped
// by the handler) and returns the new id.
func (c *Connector) CreateConnection(body interface{}) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(http.MethodPost, "/internal/sso/connection", body, &out)
	return out.ID, err
}

func (c *Connector) UpdateConnection(id string, body interface{}) error {
	return c.do(http.MethodPut, "/internal/sso/connection/"+id, body, nil)
}

func (c *Connector) DeleteConnection(id, orgID string) error {
	return c.do(http.MethodDelete, "/internal/sso/connection/"+id+"?organisation_id="+orgID, nil, nil)
}

// ── Domains ──────────────────────────────────────────────────────────

func (c *Connector) ListDomains(connID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodGet, "/internal/sso/connection/"+connID+"/domain", nil, &out)
	return out, err
}

func (c *Connector) AddDomain(connID string, body interface{}) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/internal/sso/connection/"+connID+"/domain", body, &out)
	return out, err
}

func (c *Connector) VerifyDomain(connID, domainID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/internal/sso/connection/"+connID+"/domain/"+domainID+"/verify", nil, &out)
	return out, err
}

func (c *Connector) DeleteDomain(connID, domainID string) error {
	return c.do(http.MethodDelete, "/internal/sso/connection/"+connID+"/domain/"+domainID, nil, nil)
}
