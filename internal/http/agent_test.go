package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/gin-gonic/gin"
)

// agentMock extends mockPersistence with agent-specific behaviour.
type agentMock struct {
	mockPersistence
	agents   map[string]*api.Agent
	sessions map[string]*api.AgentSession
	state    map[string]map[string]*api.AgentState
	messages []*api.AgentMessage
	users    map[string]*api.User
}

func newAgentMock() *agentMock {
	return &agentMock{
		mockPersistence: *newMockPersistence(),
		agents:          make(map[string]*api.Agent),
		sessions:        make(map[string]*api.AgentSession),
		state:           make(map[string]map[string]*api.AgentState),
		users:           make(map[string]*api.User),
	}
}

func (m *agentMock) GetUserByID(id string) (*api.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return nil, nil
}

func (m *agentMock) GetAgents(ownerID string) ([]*api.Agent, error) {
	var results []*api.Agent
	for _, a := range m.agents {
		if a.OwnerID == ownerID && a.ArchivedAt == nil {
			results = append(results, a)
		}
	}
	return results, nil
}

func (m *agentMock) GetAgentsByOrgID(orgID string) ([]*api.Agent, error) {
	var results []*api.Agent
	for _, a := range m.agents {
		if a.OrganisationID != nil && *a.OrganisationID == orgID && a.ArchivedAt == nil {
			results = append(results, a)
		}
	}
	return results, nil
}

func (m *agentMock) GetAgentByID(id string) (*api.Agent, error) {
	if a, ok := m.agents[id]; ok {
		return a, nil
	}
	return nil, nil
}

func (m *agentMock) CreateAgent(agent api.Agent) (*string, error) {
	id := "agent-new"
	agent.ID = id
	agent.CreatedAt = time.Now()
	agent.UpdatedAt = time.Now()
	m.agents[id] = &agent
	return &id, nil
}

func (m *agentMock) UpdateAgent(agent api.Agent) error {
	if existing, ok := m.agents[agent.ID]; ok {
		existing.Name = agent.Name
		existing.Description = agent.Description
	}
	return nil
}

func (m *agentMock) ArchiveAgent(id string) error {
	if a, ok := m.agents[id]; ok {
		now := time.Now()
		a.ArchivedAt = &now
		a.Status = api.AgentStatusStopped
	}
	return nil
}

func (m *agentMock) UpdateAgentStatus(id string, status string, startedAt *time.Time, stoppedAt *time.Time) error {
	if a, ok := m.agents[id]; ok {
		a.Status = status
		a.StartedAt = startedAt
		a.StoppedAt = stoppedAt
	}
	return nil
}

func (m *agentMock) CreateAgentSession(agentID string) (*string, error) {
	id := "session-new"
	m.sessions[id] = &api.AgentSession{
		ID:        id,
		AgentID:   agentID,
		StartedAt: time.Now(),
		Status:    api.AgentSessionActive,
	}
	return &id, nil
}

func (m *agentMock) GetActiveAgentSession(agentID string) (*api.AgentSession, error) {
	for _, s := range m.sessions {
		if s.AgentID == agentID && s.Status == api.AgentSessionActive {
			return s, nil
		}
	}
	return nil, nil
}

func (m *agentMock) EndAgentSession(id string, status string, errorMsg *string) error {
	if s, ok := m.sessions[id]; ok {
		now := time.Now()
		s.Status = status
		s.EndedAt = &now
	}
	return nil
}

func (m *agentMock) GetAgentState(agentID string) ([]*api.AgentState, error) {
	var results []*api.AgentState
	if stateMap, ok := m.state[agentID]; ok {
		for _, s := range stateMap {
			results = append(results, s)
		}
	}
	return results, nil
}

func (m *agentMock) GetAgentStateKey(agentID string, key string) (*api.AgentState, error) {
	if stateMap, ok := m.state[agentID]; ok {
		if s, ok := stateMap[key]; ok {
			return s, nil
		}
	}
	return nil, nil
}

func (m *agentMock) UpsertAgentState(agentID string, key string, value interface{}) error {
	if _, ok := m.state[agentID]; !ok {
		m.state[agentID] = make(map[string]*api.AgentState)
	}
	m.state[agentID][key] = &api.AgentState{
		AgentID:    agentID,
		StateKey:   key,
		StateValue: value,
		UpdatedAt:  time.Now(),
	}
	return nil
}

func (m *agentMock) DeleteAgentStateKey(agentID string, key string) error {
	if stateMap, ok := m.state[agentID]; ok {
		delete(stateMap, key)
	}
	return nil
}

func (m *agentMock) CreateAgentMessage(msg api.AgentMessage) (*string, error) {
	id := "msg-new"
	msg.ID = id
	m.messages = append(m.messages, &msg)
	return &id, nil
}

// Phase 1 agent memory stubs. These satisfy the Persistence interface for
// the existing agent HTTP handler tests. The new Phase 1 endpoints will
// get their own dedicated test file with focused mocks in Task 1.7.
func (m *agentMock) ResolveOrCreateAgentIdentity(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *agentMock) ResolveOrCreateAgentIdentityWithSecondary(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string, secondaryExternalID *string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *agentMock) ResolveOrCreateAgentConversation(agentID string, agentUserID *string, channelType, channelID string, threadID *string, idleTimeout int) (*persistence.ConversationResolution, error) {
	return nil, nil
}
func (m *agentMock) CloseAgentConversation(conversationID string) error {
	return nil
}
func (m *agentMock) GetAgentConversationByID(id string) (*api.AgentConversation, error) {
	return nil, nil
}
func (m *agentMock) GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error) {
	return nil, nil
}
func (m *agentMock) CreateAgentMessageInConversation(msg api.AgentMessage) (*string, error) {
	return nil, nil
}

// Phase 2 agent memory stubs. These satisfy the Persistence interface for
// the existing agent HTTP handler tests and for the Phase 2 focused test
// file (agent_memory_phase2_test.go) which embeds agentMock and overrides
// the methods it cares about.
func (m *agentMock) CreateAgentMemory(mem api.AgentMemory) (*string, error) {
	return nil, nil
}
func (m *agentMock) GetAgentMemoryByID(id string) (*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) GetAgentMemoriesForUser(agentUserID string, pinnedOnly bool, limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) DeleteAgentMemory(id string) error { return nil }
func (m *agentMock) TouchAgentMemoryLastUsed(id string) error {
	return nil
}
func (m *agentMock) CreateAgentPendingAction(pa api.AgentPendingAction) (*string, error) {
	return nil, nil
}
func (m *agentMock) GetAgentPendingActionByID(id string) (*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *agentMock) GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *agentMock) GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *agentMock) MarkPendingActionNotified(id string) error {
	return nil
}
func (m *agentMock) UpdatePendingActionStatus(id, status string) error {
	return nil
}
func (m *agentMock) CreateAgentCommitment(c api.AgentCommitment) (*string, error) {
	return nil, nil
}
func (m *agentMock) GetAgentCommitmentByID(id string) (*api.AgentCommitment, error) {
	return nil, nil
}
func (m *agentMock) GetDueCommitments(limit int) ([]*api.AgentCommitment, error) {
	return nil, nil
}
func (m *agentMock) GetCommitmentsForUser(agentUserID string, limit int) ([]*api.AgentCommitment, error) {
	return nil, nil
}
func (m *agentMock) UpdateCommitmentStatus(id, status string) error {
	return nil
}

func (m *agentMock) UpsertGoogleAccount(agentUserID, email, refreshToken, label, purpose string) error {
	return nil
}

func (m *agentMock) GetGoogleAccounts(agentUserID string, purpose ...string) ([]*api.AgentUserGoogleAccount, error) {
	return nil, nil
}

func (m *agentMock) GetGoogleAccountsForLinkedUsers(agentUserID string, purpose ...string) ([]*api.AgentUserGoogleAccount, error) {
	return nil, nil
}

func (m *agentMock) DeleteGoogleAccount(agentUserID, email string, purpose ...string) error {
	return nil
}

func (m *agentMock) UpsertTriggerGoogleAccount(string, string, string, string, string) error {
	return nil
}
func (m *agentMock) GetTriggerGoogleAccounts(string, ...string) ([]*api.TriggerGoogleAccount, error) {
	return nil, nil
}
func (m *agentMock) DeleteTriggerGoogleAccount(string, string, ...string) error { return nil }

// Agent schedule stubs.
func (m *agentMock) CreateAgentSchedule(s api.AgentSchedule) (*string, error) { return nil, nil }
func (m *agentMock) GetAgentSchedules(agentID string) ([]*api.AgentSchedule, error) {
	return nil, nil
}
func (m *agentMock) GetAgentSchedulesForUser(agentID, agentUserID string) ([]*api.AgentSchedule, error) {
	return nil, nil
}
func (m *agentMock) GetAgentScheduleByID(id string) (*api.AgentSchedule, error) { return nil, nil }
func (m *agentMock) UpdateAgentSchedule(s api.AgentSchedule) error              { return nil }
func (m *agentMock) DeleteAgentSchedule(id string) error                        { return nil }
func (m *agentMock) DeleteAgentScheduleByName(agentID, name string) error       { return nil }
func (m *agentMock) FindAgentScheduleByName(agentID, name string) (*api.AgentSchedule, error) {
	return nil, nil
}
func (m *agentMock) GetEnabledAgentSchedules() ([]*api.AgentSchedule, error) { return nil, nil }
func (m *agentMock) UpdateAgentScheduleLastFired(id string, firedAt time.Time) error {
	return nil
}

func setupAgentRouter(svc *Service) *gin.Engine {
	router := gin.New()
	agents := router.Group("/agent")
	agents.GET("", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.getAgents(c)
	})
	agents.GET("/:id", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.getAgentByID(c)
	})
	agents.POST("", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.createAgent(c)
	})
	agents.POST("/:id", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.updateAgent(c)
	})
	agents.DELETE("/:id", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.archiveAgent(c)
	})
	agents.POST("/:id/start", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.startAgent(c)
	})
	agents.POST("/:id/stop", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.stopAgent(c)
	})
	agents.POST("/:id/pause", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.pauseAgent(c)
	})
	agents.GET("/:id/state", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.getAgentState(c)
	})
	agents.GET("/:id/state/:key", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.getAgentStateKey(c)
	})
	agents.POST("/:id/state/:key", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.setAgentStateKey(c)
	})
	agents.DELETE("/:id/state/:key", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.deleteAgentStateKey(c)
	})
	agents.POST("/:id/message", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		svc.createAgentMessage(c)
	})
	return router
}

func Test_GetAgents_Empty_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/agent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(BeEmpty())
}

func Test_GetAgents_ReturnsOwnedAgents(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/agent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(1))
	Expect(result[0]["name"]).To(Equal("Test Agent"))
}

func Test_CreateAgent_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "My Agent",
		"description": "A helpful agent",
	})
	req := httptest.NewRequest(http.MethodPost, "/agent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["id"]).To(Equal("agent-new"))
}

func Test_CreateAgent_MissingName_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{})
	req := httptest.NewRequest(http.MethodPost, "/agent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
}

func Test_GetAgentByID_NotFound(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/agent/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
}

func Test_GetAgentByID_Forbidden_WrongOwner(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Other's Agent", OwnerID: "user-2",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/agent/agent-1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusForbidden))
}

func Test_StartAgent_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/start", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["status"]).To(Equal("running"))
	Expect(result["session_id"]).To(Equal("session-new"))

	// Verify agent status was updated
	Expect(mock.agents["agent-1"].Status).To(Equal(api.AgentStatusRunning))
}

func Test_StartAgent_AlreadyRunning_ReturnsConflict(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusRunning, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/start", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusConflict))
}

func Test_StopAgent_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	now := time.Now()
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusRunning, StartedAt: &now,
		Channels: json.RawMessage("[]"),
	}
	mock.sessions["session-1"] = &api.AgentSession{
		ID: "session-1", AgentID: "agent-1", Status: api.AgentSessionActive,
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/stop", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.agents["agent-1"].Status).To(Equal(api.AgentStatusStopped))
	Expect(mock.sessions["session-1"].Status).To(Equal(api.AgentSessionEnded))
}

func Test_PauseAgent_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	now := time.Now()
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusRunning, StartedAt: &now,
		Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/pause", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.agents["agent-1"].Status).To(Equal(api.AgentStatusPaused))
}

func Test_AgentState_SetAndGet(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	// Set state
	body, _ := json.Marshal(map[string]interface{}{"value": map[string]string{"conversation": "hello"}})
	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/state/memory", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	Expect(w.Code).To(Equal(http.StatusOK))

	// Get state
	req = httptest.NewRequest(http.MethodGet, "/agent/agent-1/state/memory", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	Expect(w.Code).To(Equal(http.StatusOK))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["state_key"]).To(Equal("memory"))
}

func Test_AgentState_GetNonExistent_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/agent/agent-1/state/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
}

func Test_AgentState_Delete(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}
	mock.state["agent-1"] = map[string]*api.AgentState{
		"memory": {AgentID: "agent-1", StateKey: "memory", StateValue: "test"},
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodDelete, "/agent/agent-1/state/memory", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.state["agent-1"]).To(BeEmpty())
}

func Test_ArchiveAgent_StopsRunningAgent(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	now := time.Now()
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusRunning, StartedAt: &now,
		Channels: json.RawMessage("[]"),
	}
	mock.sessions["session-1"] = &api.AgentSession{
		ID: "session-1", AgentID: "agent-1", Status: api.AgentSessionActive,
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	req := httptest.NewRequest(http.MethodDelete, "/agent/agent-1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.agents["agent-1"].ArchivedAt).NotTo(BeNil())
	Expect(mock.sessions["session-1"].Status).To(Equal(api.AgentSessionEnded))
}

func Test_CreateAgentMessage_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Test Agent", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{
		"direction":    "inbound",
		"channel_type": "telegram",
		"content":      "Hello agent!",
	})
	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["id"]).To(Equal("msg-new"))
}

func Test_UpdateAgent_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.agents["agent-1"] = &api.Agent{
		ID: "agent-1", Name: "Old Name", OwnerID: "user-1",
		Status: api.AgentStatusStopped, Channels: json.RawMessage("[]"),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupAgentRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{"name": "New Name"})
	req := httptest.NewRequest(http.MethodPost, "/agent/agent-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.agents["agent-1"].Name).To(Equal("New Name"))
}

func Test_CanAccessAgent_OrgMember(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc := &Service{}
	orgID := "org-1"
	user := &api.User{
		ID:            "user-1",
		Organisations: []api.Organisation{{ID: "org-1"}},
	}
	agent := &api.Agent{
		ID: "agent-1", OwnerID: "user-2", OrganisationID: &orgID,
	}

	Expect(svc.canAccessAgent(user, agent)).To(BeTrue())
}

func Test_CanAccessAgent_NoAccess(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	svc := &Service{}
	orgID := "org-2"
	user := &api.User{
		ID:            "user-1",
		Organisations: []api.Organisation{{ID: "org-1"}},
	}
	agent := &api.Agent{
		ID: "agent-1", OwnerID: "user-2", OrganisationID: &orgID,
	}

	Expect(svc.canAccessAgent(user, agent)).To(BeFalse())
}

// Phase 5 stubs
func (m *agentMock) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) {
	return nil, nil
}
func (m *agentMock) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *agentMock) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error { return nil }
func (m *agentMock) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) {
	return nil, nil
}

// Phase 4 stubs
func (m *agentMock) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *agentMock) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) {
	return nil, nil
}
func (m *agentMock) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) {
	return nil, nil
}
func (m *agentMock) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *agentMock) DeleteAllMemoriesForUser(agentUserID string) (int64, error)  { return 0, nil }
func (m *agentMock) GetExpiredMemories(limit int) ([]*api.AgentMemory, error)    { return nil, nil }
func (m *agentMock) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) {
	return 0, nil
}
func (m *agentMock) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *agentMock) GetAgentsWithRetentionPolicy() ([]struct {
	ID                  string `db:"id"`
	MemoryRetentionDays int    `db:"memory_retention_days"`
}, error) {
	return nil, nil
}
func (m *agentMock) UpdateAgentRetentionDays(agentID string, days *int) error     { return nil }
func (m *agentMock) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) { return nil, nil }
func (m *agentMock) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	return nil, nil
}
func (m *agentMock) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	return nil, nil
}
func (m *agentMock) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *agentMock) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) {
	return nil, nil
}

// Phase 7 stubs
func (m *agentMock) FindContradictionCandidates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) FindNearDuplicates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, excludeID string, limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMock) SupersedeMemory(oldID, newID string) error           { return nil }
func (m *agentMock) MergeMemory(duplicateID, canonicalID string) error   { return nil }
func (m *agentMock) CountPinnedMemories(agentUserID string) (int, error) { return 0, nil }
func (m *agentMock) UnpinOldestMemories(agentUserID string, count int) ([]string, error) {
	return nil, nil
}
func (m *agentMock) GetMaxPinnedMemories(agentID string) (int, error)         { return 50, nil }
func (m *agentMock) UpdateMaxPinnedMemories(agentID string, limit *int) error { return nil }
func (m *agentMock) GetAgentByOrchestratorFloID(flowID string) (*api.Agent, error) {
	return nil, nil
}
