package persistence

import (
	"database/sql"
	"encoding/json"
	"time"

	"flomation.app/automate/api"
)

// GetAgents returns all non-archived agents for a user (personal mode).
func (s *Service) GetAgents(ownerID string) ([]*api.Agent, error) {
	var results []*api.Agent
	if err := s.stmtGetAgents.Select(&results, struct {
		OwnerID string `db:"owner_id"`
	}{OwnerID: ownerID}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAgentsByOrgID returns all non-archived agents for an organisation.
func (s *Service) GetAgentsByOrgID(organisationID string) ([]*api.Agent, error) {
	var results []*api.Agent
	if err := s.stmtGetAgentsByOrgID.Select(&results, struct {
		OrganisationID string `db:"organisation_id"`
	}{OrganisationID: organisationID}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAgentByID returns an agent by ID, including computed fields.
func (s *Service) GetAgentByID(id string) (*api.Agent, error) {
	var agent api.Agent
	if err := s.stmtGetAgentByID.Get(&agent, struct {
		ID         string `db:"id"`
		EncryptKey string `db:"encrypt_key"`
	}{ID: id, EncryptKey: s.config.Database.EncryptionKey}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &agent, nil
}

// IsFlowAgentPaused checks if the given flow ID is the orchestrator or
// extraction flow of a non-running agent. Returns true if the agent
// exists and is paused, stopped, or in error state — false if running
// or if no agent owns this flow.
func (s *Service) IsFlowAgentPaused(flowID string) bool {
	var status string
	err := s.conn.Get(&status, `
		SELECT status FROM agents
		WHERE (orchestrator_flow_id = $1 OR extraction_flow_id = $1)
		LIMIT 1`, flowID)
	if err != nil {
		return false // No agent owns this flow, or query failed — allow execution.
	}
	return status != api.AgentStatusRunning
}

// CreateAgent creates a new agent and returns its ID.
func (s *Service) CreateAgent(agent api.Agent) (*string, error) {
	channelsJSON := agent.Channels
	if channelsJSON == nil {
		channelsJSON = json.RawMessage("[]")
	}

	var id string
	if err := s.stmtCreateAgent.Get(&id, struct {
		Name                    string          `db:"name"`
		Description             *string         `db:"description"`
		OwnerID                 string          `db:"owner_id"`
		OrganisationID          *string         `db:"organisation_id"`
		EnvironmentID           *string         `db:"environment_id"`
		QueueID                 *string         `db:"queue_id"`
		SystemPrompt            *string         `db:"system_prompt"`
		OrchestratorFlowID      *string         `db:"orchestrator_flow_id"`
		ExtractionFlowID        *string         `db:"extraction_flow_id"`
		AIAPIKey                *string         `db:"ai_api_key"`
		EncryptKey              string          `db:"encrypt_key"`
		MaxConcurrentExecutions int             `db:"max_concurrent_executions"`
		IdleTimeoutSeconds      int             `db:"idle_timeout_seconds"`
		Channels                json.RawMessage `db:"channels"`
		RequiresApproval        bool            `db:"requires_approval"`
		MaxExecutionsPerHour    int             `db:"max_executions_per_hour"`
	}{
		Name:                    agent.Name,
		Description:             agent.Description,
		OwnerID:                 agent.OwnerID,
		OrganisationID:          agent.OrganisationID,
		EnvironmentID:           agent.EnvironmentID,
		QueueID:                 agent.QueueID,
		SystemPrompt:            agent.SystemPrompt,
		OrchestratorFlowID:      agent.OrchestratorFlowID,
		ExtractionFlowID:        agent.ExtractionFlowID,
		AIAPIKey:                agent.AIAPIKey,
		EncryptKey:              s.config.Database.EncryptionKey,
		MaxConcurrentExecutions: agent.MaxConcurrentExecutions,
		IdleTimeoutSeconds:      agent.IdleTimeoutSeconds,
		Channels:                channelsJSON,
		RequiresApproval:        agent.RequiresApproval,
		MaxExecutionsPerHour:    agent.MaxExecutionsPerHour,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// UpdateAgent updates an existing agent's configuration.
func (s *Service) UpdateAgent(agent api.Agent) error {
	channelsJSON := agent.Channels
	if channelsJSON == nil {
		channelsJSON = json.RawMessage("[]")
	}

	_, err := s.stmtUpdateAgent.Exec(struct {
		ID                      string          `db:"id"`
		Name                    string          `db:"name"`
		Description             *string         `db:"description"`
		EnvironmentID           *string         `db:"environment_id"`
		QueueID                 *string         `db:"queue_id"`
		SystemPrompt            *string         `db:"system_prompt"`
		OrchestratorFlowID      *string         `db:"orchestrator_flow_id"`
		AIAPIKey                *string         `db:"ai_api_key"`
		EncryptKey              string          `db:"encrypt_key"`
		MaxConcurrentExecutions int             `db:"max_concurrent_executions"`
		IdleTimeoutSeconds      int             `db:"idle_timeout_seconds"`
		Channels                json.RawMessage `db:"channels"`
		RequiresApproval        bool            `db:"requires_approval"`
		MaxExecutionsPerHour    int             `db:"max_executions_per_hour"`
	}{
		ID:                      agent.ID,
		Name:                    agent.Name,
		Description:             agent.Description,
		EnvironmentID:           agent.EnvironmentID,
		QueueID:                 agent.QueueID,
		SystemPrompt:            agent.SystemPrompt,
		OrchestratorFlowID:      agent.OrchestratorFlowID,
		AIAPIKey:                agent.AIAPIKey,
		EncryptKey:              s.config.Database.EncryptionKey,
		MaxConcurrentExecutions: agent.MaxConcurrentExecutions,
		IdleTimeoutSeconds:      agent.IdleTimeoutSeconds,
		Channels:                channelsJSON,
		RequiresApproval:        agent.RequiresApproval,
		MaxExecutionsPerHour:    agent.MaxExecutionsPerHour,
	})
	return err
}

// ArchiveAgent soft-deletes an agent and stops it.
func (s *Service) ArchiveAgent(id string) error {
	_, err := s.stmtArchiveAgent.Exec(struct {
		ID string `db:"id"`
	}{ID: id})
	return err
}

// UpdateAgentStatus updates the running status of an agent.
func (s *Service) UpdateAgentStatus(id string, status string, startedAt *time.Time, stoppedAt *time.Time) error {
	_, err := s.stmtUpdateAgentStatus.Exec(struct {
		ID        string     `db:"id"`
		Status    string     `db:"status"`
		StartedAt *time.Time `db:"started_at"`
		StoppedAt *time.Time `db:"stopped_at"`
	}{ID: id, Status: status, StartedAt: startedAt, StoppedAt: stoppedAt})
	return err
}

// --- Sessions ---

// CreateAgentSession creates a new session for an agent and returns the session ID.
func (s *Service) CreateAgentSession(agentID string) (*string, error) {
	var id string
	if err := s.stmtCreateAgentSession.Get(&id, struct {
		AgentID string `db:"agent_id"`
	}{AgentID: agentID}); err != nil {
		return nil, err
	}
	return &id, nil
}

// EndAgentSession marks a session as ended or crashed.
func (s *Service) EndAgentSession(id string, status string, errorMessage *string) error {
	_, err := s.stmtEndAgentSession.Exec(struct {
		ID           string  `db:"id"`
		Status       string  `db:"status"`
		ErrorMessage *string `db:"error_message"`
	}{ID: id, Status: status, ErrorMessage: errorMessage})
	return err
}

// UpdateAgentSessionHeartbeat refreshes the heartbeat timestamp and summary.
func (s *Service) UpdateAgentSessionHeartbeat(id string, summary interface{}) error {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		summaryJSON = []byte("{}")
	}
	_, err = s.stmtUpdateAgentSessionHeartbeat.Exec(struct {
		ID      string          `db:"id"`
		Summary json.RawMessage `db:"summary"`
	}{ID: id, Summary: summaryJSON})
	return err
}

// GetAgentSessions returns paginated sessions for an agent.
func (s *Service) GetAgentSessions(agentID string, limit int, offset int) ([]*api.AgentSession, error) {
	var results []*api.AgentSession
	if err := s.stmtGetAgentSessions.Select(&results, struct {
		AgentID string `db:"agent_id"`
		Limit   int    `db:"limit"`
		Offset  int    `db:"offset"`
	}{AgentID: agentID, Limit: limit, Offset: offset}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAgentSessionByID returns a specific session.
func (s *Service) GetAgentSessionByID(id string) (*api.AgentSession, error) {
	var session api.AgentSession
	if err := s.stmtGetAgentSessionByID.Get(&session, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// GetActiveAgentSession returns the currently active session for an agent, if any.
func (s *Service) GetActiveAgentSession(agentID string) (*api.AgentSession, error) {
	var session api.AgentSession
	if err := s.stmtGetActiveAgentSession.Get(&session, struct {
		AgentID string `db:"agent_id"`
	}{AgentID: agentID}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// --- State ---

// GetAgentState returns all state keys for an agent.
func (s *Service) GetAgentState(agentID string) ([]*api.AgentState, error) {
	var results []*api.AgentState
	if err := s.stmtGetAgentState.Select(&results, struct {
		AgentID string `db:"agent_id"`
	}{AgentID: agentID}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAgentStateKey returns a specific state key.
func (s *Service) GetAgentStateKey(agentID string, key string) (*api.AgentState, error) {
	var state api.AgentState
	if err := s.stmtGetAgentStateKey.Get(&state, struct {
		AgentID  string `db:"agent_id"`
		StateKey string `db:"state_key"`
	}{AgentID: agentID, StateKey: key}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

// UpsertAgentState creates or updates a state key.
func (s *Service) UpsertAgentState(agentID string, key string, value interface{}) error {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.stmtUpsertAgentState.Exec(struct {
		AgentID    string          `db:"agent_id"`
		StateKey   string          `db:"state_key"`
		StateValue json.RawMessage `db:"state_value"`
	}{AgentID: agentID, StateKey: key, StateValue: valueJSON})
	return err
}

// DeleteAgentStateKey removes a specific state key.
func (s *Service) DeleteAgentStateKey(agentID string, key string) error {
	_, err := s.stmtDeleteAgentStateKey.Exec(struct {
		AgentID  string `db:"agent_id"`
		StateKey string `db:"state_key"`
	}{AgentID: agentID, StateKey: key})
	return err
}

// --- Messages ---

// GetAgentMessages returns paginated messages for an agent.
func (s *Service) GetAgentMessages(agentID string, limit int, offset int) ([]*api.AgentMessage, error) {
	var results []*api.AgentMessage
	if err := s.stmtGetAgentMessages.Select(&results, struct {
		AgentID string `db:"agent_id"`
		Limit   int    `db:"limit"`
		Offset  int    `db:"offset"`
	}{AgentID: agentID, Limit: limit, Offset: offset}); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAgentSessionMessages returns paginated messages for a specific session (chronological order).
func (s *Service) GetAgentSessionMessages(sessionID string, limit int, offset int) ([]*api.AgentMessage, error) {
	var results []*api.AgentMessage
	if err := s.stmtGetAgentSessionMessages.Select(&results, struct {
		SessionID string `db:"session_id"`
		Limit     int    `db:"limit"`
		Offset    int    `db:"offset"`
	}{SessionID: sessionID, Limit: limit, Offset: offset}); err != nil {
		return nil, err
	}
	return results, nil
}

// CreateAgentMessage records an inbound or outbound message.
func (s *Service) CreateAgentMessage(msg api.AgentMessage) (*string, error) {
	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	var id string
	if err := s.stmtCreateAgentMessage.Get(&id, struct {
		AgentID     string          `db:"agent_id"`
		SessionID   *string         `db:"session_id"`
		Direction   string          `db:"direction"`
		ChannelType string          `db:"channel_type"`
		Sender      *string         `db:"sender"`
		Content     string          `db:"content"`
		Metadata    json.RawMessage `db:"metadata"`
		ExecutionID *string         `db:"execution_id"`
	}{
		AgentID:     msg.AgentID,
		SessionID:   msg.SessionID,
		Direction:   msg.Direction,
		ChannelType: msg.ChannelType,
		Sender:      msg.Sender,
		Content:     msg.Content,
		Metadata:    metadataJSON,
		ExecutionID: msg.ExecutionID,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// --- Agent Executions ---

// GetAgentExecutions returns paginated executions dispatched by an agent.
func (s *Service) GetAgentExecutions(agentID string, limit int, offset int) ([]*api.AgentExecution, error) {
	var results []*api.AgentExecution
	if err := s.stmtGetAgentExecutions.Select(&results, struct {
		AgentID string `db:"agent_id"`
		Limit   int    `db:"limit"`
		Offset  int    `db:"offset"`
	}{AgentID: agentID, Limit: limit, Offset: offset}); err != nil {
		return nil, err
	}
	return results, nil
}

// CreateAgentExecution records a flow execution dispatched by an agent.
func (s *Service) CreateAgentExecution(exec api.AgentExecution) (*string, error) {
	var id string
	if err := s.stmtCreateAgentExecution.Get(&id, struct {
		AgentID          string  `db:"agent_id"`
		SessionID        *string `db:"session_id"`
		MessageID        *string `db:"message_id"`
		ExecutionID      string  `db:"execution_id"`
		FlowID           string  `db:"flow_id"`
		Status           string  `db:"status"`
		RequiresApproval bool    `db:"requires_approval"`
	}{
		AgentID:          exec.AgentID,
		SessionID:        exec.SessionID,
		MessageID:        exec.MessageID,
		ExecutionID:      exec.ExecutionID,
		FlowID:           exec.FlowID,
		Status:           exec.Status,
		RequiresApproval: exec.RequiresApproval,
	}); err != nil {
		return nil, err
	}
	return &id, nil
}

// UpdateAgentExecutionStatus updates the status of an agent execution (approve/reject/complete).
func (s *Service) UpdateAgentExecutionStatus(id string, status string, approvedBy *string, completedAt *time.Time) error {
	var approvedAt *time.Time
	if approvedBy != nil {
		now := time.Now()
		approvedAt = &now
	}
	_, err := s.stmtUpdateAgentExecutionStatus.Exec(struct {
		ID          string     `db:"id"`
		Status      string     `db:"status"`
		ApprovedBy  *string    `db:"approved_by"`
		ApprovedAt  *time.Time `db:"approved_at"`
		CompletedAt *time.Time `db:"completed_at"`
	}{ID: id, Status: status, ApprovedBy: approvedBy, ApprovedAt: approvedAt, CompletedAt: completedAt})
	return err
}

// CountAgentExecutionsInHour returns the number of executions dispatched in the last hour.
func (s *Service) CountAgentExecutionsInHour(agentID string) (int64, error) {
	var count int64
	if err := s.stmtCountAgentExecutionsInHour.Get(&count, struct {
		AgentID string `db:"agent_id"`
	}{AgentID: agentID}); err != nil {
		return 0, err
	}
	return count, nil
}
