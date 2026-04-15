package poller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPFlowDispatcher dispatches flows by calling the API's own internal
// execution endpoints. This keeps the dispatch logic consistent with
// what Launch uses, and avoids coupling the poller to the HTTP handler's
// internal implementation details.
type HTTPFlowDispatcher struct {
	apiURL string
	client *http.Client
}

// NewHTTPFlowDispatcher creates a dispatcher that calls the given API base URL.
func NewHTTPFlowDispatcher(apiURL string) *HTTPFlowDispatcher {
	return &HTTPFlowDispatcher{
		apiURL: apiURL,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// DispatchFlow triggers a flow execution via the internal API.
func (d *HTTPFlowDispatcher) DispatchFlow(flowID string, triggerID *string, data map[string]interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	var endpoint string
	if triggerID != nil && *triggerID != "" {
		endpoint = fmt.Sprintf("%s/api/v1/internal/flo/%s/trigger/%s/execute", d.apiURL, flowID, *triggerID)
	} else {
		endpoint = fmt.Sprintf("%s/api/v1/internal/flo/%s/execute", d.apiURL, flowID)
	}

	resp, err := d.client.Post(endpoint, "application/json", bytes.NewReader(payload)) // #nosec G107 — internal self-call
	if err != nil {
		return fmt.Errorf("dispatch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("dispatch returned %d: %s", resp.StatusCode, string(rb))
	}
	return nil
}
