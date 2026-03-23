package launch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"flomation.app/automate/api/internal/config"
	log "github.com/sirupsen/logrus"
)

type triggerPayload struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data"`
	FlowID string          `json:"flow_id"`
}

type Connector struct {
	config *config.Config
	client *http.Client
}

func NewConnector(config *config.Config) *Connector {
	return &Connector{
		config: config,
		client: &http.Client{
			Timeout: time.Second * 10,
		},
	}
}

func (c *Connector) RegisterTrigger(id, typeName string, data []byte, flowID string, authToken string) error {
	payload := triggerPayload{
		ID:     id,
		Type:   typeName,
		Data:   data,
		FlowID: flowID,
	}

	if payload.Data == nil {
		payload.Data = json.RawMessage("{}")
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("unable to marshal trigger payload: %w", err)
	}

	url := fmt.Sprintf("%v/trigger/%v", c.config.Launch.URL, id)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("unable to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to register trigger with launch service: %w", err)
	}

	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("launch service returned status %v for trigger %v", resp.Status, id)
	}

	return nil
}

func (c *Connector) DisableTrigger(id string, authToken string) error {
	url := fmt.Sprintf("%v/trigger/%v", c.config.Launch.URL, id)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("unable to create request: %w", err)
	}

	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to disable trigger on launch service: %w", err)
	}

	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("launch service returned status %v for trigger deletion %v", resp.Status, id)
	}

	log.WithFields(log.Fields{
		"trigger_id": id,
	}).Info("disabled trigger on launch service")

	return nil
}
