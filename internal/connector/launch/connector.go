package launch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/mtls"
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

// Client returns the HTTP client used by this connector.
// Other API components that need to call Launch can use this
// to share the mTLS configuration.
func (c *Connector) Client() *http.Client {
	return c.client
}

func NewConnector(cfg *config.Config) *Connector {
	client, err := mtls.ClientOrDefault(cfg.TLS, 10*time.Second)
	if err != nil {
		log.WithError(err).Fatal("launch connector: unable to create mTLS client")
	}

	return &Connector{
		config: cfg,
		client: client,
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

	url := fmt.Sprintf("%v/trigger/%v", c.config.InternalLaunchURL(), id)
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
	url := fmt.Sprintf("%v/trigger/%v", c.config.InternalLaunchURL(), id)
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

// agentRegistrationPayload is the JSON body sent to Launch when registering an agent.
type agentRegistrationPayload struct {
	AgentID              string          `json:"agent_id"`
	OrchestratorFlowID   *string         `json:"orchestrator_flow_id"`
	TriggerID            *string         `json:"trigger_id"`
	Channels             json.RawMessage `json:"channels"`
	EnvironmentID        *string         `json:"environment_id"`
	MaxExecutionsPerHour int             `json:"max_executions_per_hour"`
	RequiresApproval     bool            `json:"requires_approval"`
	SystemPrompt         *string         `json:"system_prompt,omitempty"`
	APIURL               string          `json:"api_url"`
}

// RegisterAgent registers an agent with the Launch service for runtime management.
func (c *Connector) RegisterAgent(agentID string, orchestratorFlowID *string, triggerID *string,
	channels json.RawMessage, environmentID *string, maxExecPerHour int, requiresApproval bool, systemPrompt *string) error {

	payload := agentRegistrationPayload{
		AgentID:              agentID,
		OrchestratorFlowID:   orchestratorFlowID,
		TriggerID:            triggerID,
		Channels:             channels,
		EnvironmentID:        environmentID,
		MaxExecutionsPerHour: maxExecPerHour,
		RequiresApproval:     requiresApproval,
		SystemPrompt:         systemPrompt,
		APIURL:               c.config.Launch.APIURL,
	}

	if payload.Channels == nil {
		payload.Channels = json.RawMessage("[]")
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("unable to marshal agent payload: %w", err)
	}

	url := fmt.Sprintf("%v/agent/%v", c.config.InternalLaunchURL(), agentID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("unable to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to register agent with launch service: %w", err)
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("launch service returned status %v for agent %v", resp.Status, agentID)
	}

	log.WithFields(log.Fields{
		"agent_id": agentID,
	}).Info("registered agent with launch service")

	return nil
}

// ChannelAction proxies a channel-specific SDK action (e.g. typing indicator)
// to the Launch service, which handles the actual channel API call.
func (c *Connector) ChannelAction(agentID, channelType, action, chatID string) error {
	payload := map[string]string{
		"channel_type": channelType,
		"action":       action,
		"chat_id":      chatID,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("unable to marshal channel action: %w", err)
	}

	url := fmt.Sprintf("%v/internal/agent/%v/channel-action", c.config.InternalLaunchURL(), agentID)
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("unable to send channel action: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("launch returned %v for channel action", resp.Status)
	}
	return nil
}

// DeregisterAgent deregisters an agent from the Launch service.
func (c *Connector) DeregisterAgent(agentID string) error {
	url := fmt.Sprintf("%v/agent/%v", c.config.InternalLaunchURL(), agentID)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("unable to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to deregister agent from launch service: %w", err)
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("launch service returned status %v for agent deregistration %v", resp.Status, agentID)
	}

	log.WithFields(log.Fields{
		"agent_id": agentID,
	}).Info("deregistered agent from launch service")

	return nil
}
