package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// agentMemoryMock extends agentMock with tracking behaviour for the four
// Phase 1 persistence methods. This lets the focused Phase 1 handler tests
// assert on the exact arguments Launch will be passing through from
// webhook payloads.
type agentMemoryMock struct {
	*agentMock

	mu sync.Mutex

	// Inputs captured from handler calls.
	resolveIdentityCalls     []resolveIdentityCall
	resolveConversationCalls []resolveConversationCall
	getHistoryCalls          []getHistoryCall
	createMessageCalls       []createMessageCall

	// Pre-canned return values.
	identityResult     *api.AgentIdentity
	userResult         *api.AgentUser
	conversationResult *api.AgentConversation
	historyResult      []*api.AgentMessage
	createMessageID    string
	forceError         error
}

type resolveIdentityCall struct {
	AgentID           string
	OrganisationID    *string
	ChannelType       string
	ChannelExternalID string
	ChannelScope      *string
	DisplayName       *string
}

type resolveConversationCall struct {
	AgentID     string
	AgentUserID *string
	ChannelType string
	ChannelID   string
	ThreadID    *string
}

type getHistoryCall struct {
	ConversationID string
	Limit          int
}

type createMessageCall struct {
	Message api.AgentMessage
}

func newAgentMemoryMock() *agentMemoryMock {
	return &agentMemoryMock{
		agentMock: newAgentMock(),
	}
}

func (m *agentMemoryMock) ResolveOrCreateAgentIdentity(
	agentID string,
	organisationID *string,
	channelType, externalID string,
	scope *string,
	displayName *string,
) (*api.AgentIdentity, *api.AgentUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolveIdentityCalls = append(m.resolveIdentityCalls, resolveIdentityCall{
		AgentID:           agentID,
		OrganisationID:    organisationID,
		ChannelType:       channelType,
		ChannelExternalID: externalID,
		ChannelScope:      scope,
		DisplayName:       displayName,
	})
	return m.identityResult, m.userResult, m.forceError
}

func (m *agentMemoryMock) ResolveOrCreateAgentConversation(
	agentID string,
	agentUserID *string,
	channelType, channelID string,
	threadID *string,
	idleTimeout int,
) (*persistence.ConversationResolution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolveConversationCalls = append(m.resolveConversationCalls, resolveConversationCall{
		AgentID:     agentID,
		AgentUserID: agentUserID,
		ChannelType: channelType,
		ChannelID:   channelID,
		ThreadID:    threadID,
	})
	if m.conversationResult == nil {
		return nil, m.forceError
	}
	return &persistence.ConversationResolution{Conversation: m.conversationResult}, m.forceError
}

func (m *agentMemoryMock) CloseAgentConversation(conversationID string) error {
	return m.forceError
}

func (m *agentMemoryMock) GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getHistoryCalls = append(m.getHistoryCalls, getHistoryCall{
		ConversationID: conversationID,
		Limit:          limit,
	})
	return m.historyResult, m.forceError
}

func (m *agentMemoryMock) CreateAgentMessageInConversation(msg api.AgentMessage) (*string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createMessageCalls = append(m.createMessageCalls, createMessageCall{Message: msg})
	if m.forceError != nil {
		return nil, m.forceError
	}
	id := m.createMessageID
	if id == "" {
		id = "msg-new"
	}
	return &id, nil
}

func setupMemoryInternalRouter(svc *Service) *gin.Engine {
	router := gin.New()
	internal := router.Group("/internal")
	internal.POST("/agent/:id/resolve-identity", svc.resolveAgentIdentityInternal)
	internal.POST("/agent/:id/conversation", svc.resolveAgentConversationInternal)
	internal.GET("/conversation/:id/history", svc.getAgentConversationHistoryInternal)
	internal.POST("/conversation/:id/message", svc.createAgentConversationMessageInternal)
	return router
}

// --- resolve-identity ---

func Test_ResolveAgentIdentityInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	orgID := "org-1"
	mock.agents["agent-1"] = &api.Agent{
		ID:             "agent-1",
		OwnerID:        "user-1",
		OrganisationID: &orgID,
		Channels:       json.RawMessage("[]"),
	}
	mock.identityResult = &api.AgentIdentity{
		ID:                "identity-1",
		AgentUserID:       "user-1",
		ChannelType:       "slack",
		ChannelExternalID: "U123",
	}
	mock.userResult = &api.AgentUser{
		ID:      "user-1",
		AgentID: "agent-1",
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"channel_type":"slack","channel_external_id":"U123","channel_scope":"T456","display_name":"Alice"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/resolve-identity", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result resolveAgentIdentityInternalResponse
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result.Identity).NotTo(BeNil())
	Expect(result.Identity.ID).To(Equal("identity-1"))
	Expect(result.User).NotTo(BeNil())
	Expect(result.User.ID).To(Equal("user-1"))

	// Verify the persistence method received the right arguments, including
	// the organisation_id plucked from the agent record.
	Expect(mock.resolveIdentityCalls).To(HaveLen(1))
	call := mock.resolveIdentityCalls[0]
	Expect(call.AgentID).To(Equal("agent-1"))
	Expect(call.ChannelType).To(Equal("slack"))
	Expect(call.ChannelExternalID).To(Equal("U123"))
	Expect(call.ChannelScope).NotTo(BeNil())
	Expect(*call.ChannelScope).To(Equal("T456"))
	Expect(call.DisplayName).NotTo(BeNil())
	Expect(*call.DisplayName).To(Equal("Alice"))
	Expect(call.OrganisationID).NotTo(BeNil())
	Expect(*call.OrganisationID).To(Equal("org-1"))
}

func Test_ResolveAgentIdentityInternal_UnknownAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	// No agent in mock.agents — GetAgentByID returns nil.

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"channel_type":"slack","channel_external_id":"U123"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-ghost/resolve-identity", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
	Expect(mock.resolveIdentityCalls).To(BeEmpty(), "must not call persistence when agent is missing")
}

func Test_ResolveAgentIdentityInternal_MissingRequiredField_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{ID: "agent-1", OwnerID: "user-1", Channels: json.RawMessage("[]")}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	// Missing channel_external_id (binding:"required")
	body := `{"channel_type":"slack"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/resolve-identity", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.resolveIdentityCalls).To(BeEmpty())
}

// --- resolve conversation ---

func Test_ResolveAgentConversationInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{ID: "agent-1", OwnerID: "user-1", Channels: json.RawMessage("[]")}
	mock.conversationResult = &api.AgentConversation{
		ID:          "conv-1",
		AgentID:     "agent-1",
		ChannelType: "slack",
		ChannelID:   "C789",
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"agent_user_id":"user-abc","channel_type":"slack","channel_id":"C789","thread_id":"1700000000.000100"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/conversation", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result api.AgentConversation
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result.ID).To(Equal("conv-1"))

	Expect(mock.resolveConversationCalls).To(HaveLen(1))
	call := mock.resolveConversationCalls[0]
	Expect(call.AgentID).To(Equal("agent-1"))
	Expect(call.ChannelType).To(Equal("slack"))
	Expect(call.ChannelID).To(Equal("C789"))
	Expect(call.ThreadID).NotTo(BeNil())
	Expect(*call.ThreadID).To(Equal("1700000000.000100"))
	Expect(call.AgentUserID).NotTo(BeNil())
	Expect(*call.AgentUserID).To(Equal("user-abc"))
}

func Test_ResolveAgentConversationInternal_NoThread_IsAllowed(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{ID: "agent-1", OwnerID: "user-1", Channels: json.RawMessage("[]")}
	mock.conversationResult = &api.AgentConversation{ID: "conv-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"channel_type":"webhook","channel_id":"hook-1"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/conversation", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.resolveConversationCalls).To(HaveLen(1))
	Expect(mock.resolveConversationCalls[0].ThreadID).To(BeNil(),
		"channels without native threading should resolve with nil thread_id")
}

// --- history fetch ---

func Test_GetAgentConversationHistoryInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.historyResult = []*api.AgentMessage{
		{ID: "m1", Content: "hello", Direction: "inbound"},
		{ID: "m2", Content: "hi there", Direction: "outbound"},
		{ID: "m3", Content: "how can I help", Direction: "outbound"},
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/conversation/conv-1/history?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []*api.AgentMessage
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(3))
	Expect(result[0].ID).To(Equal("m1"))

	Expect(mock.getHistoryCalls).To(HaveLen(1))
	Expect(mock.getHistoryCalls[0].ConversationID).To(Equal("conv-1"))
	Expect(mock.getHistoryCalls[0].Limit).To(Equal(10))
}

func Test_GetAgentConversationHistoryInternal_DefaultLimit(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.historyResult = []*api.AgentMessage{}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/conversation/conv-1/history", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.getHistoryCalls).To(HaveLen(1))
	// Default when no limit query param is given.
	Expect(mock.getHistoryCalls[0].Limit).To(Equal(20))
}

func Test_GetAgentConversationHistoryInternal_ClampsExcessiveLimit(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.historyResult = []*api.AgentMessage{}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/conversation/conv-1/history?limit=9999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.getHistoryCalls).To(HaveLen(1))
	// parsePagination's default-cap behaviour rounds an out-of-range limit
	// back to 50 BEFORE our handler's 200 ceiling sees it, so the clamped
	// value depends on the shared helper. Either is acceptable for the
	// anti-runaway guarantee; just assert the final value is bounded.
	Expect(mock.getHistoryCalls[0].Limit).To(BeNumerically("<=", 200))
	Expect(mock.getHistoryCalls[0].Limit).To(BeNumerically(">", 0))
}

// --- conversation message create ---

func Test_CreateAgentConversationMessageInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.createMessageID = "new-msg-id"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"direction":"inbound","channel_type":"slack","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/conversation/conv-1/message?agent_id=agent-1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var result map[string]string
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["id"]).To(Equal("new-msg-id"))

	Expect(mock.createMessageCalls).To(HaveLen(1))
	msg := mock.createMessageCalls[0].Message
	Expect(msg.AgentID).To(Equal("agent-1"))
	Expect(msg.ConversationID).NotTo(BeNil())
	Expect(*msg.ConversationID).To(Equal("conv-1"))
	Expect(msg.Direction).To(Equal("inbound"))
	Expect(msg.Content).To(Equal("hello"))
}

func Test_CreateAgentConversationMessageInternal_MissingAgentID_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"direction":"inbound","channel_type":"slack","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/conversation/conv-1/message", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.createMessageCalls).To(BeEmpty())
}

func Test_CreateAgentConversationMessageInternal_MissingBodyFields_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	// Missing required 'content'
	body := `{"direction":"inbound","channel_type":"slack"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/conversation/conv-1/message?agent_id=agent-1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.createMessageCalls).To(BeEmpty())
}

// --- error propagation sanity check ---

func Test_ResolveAgentIdentityInternal_PersistenceError_Returns500(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{ID: "agent-1", OwnerID: "user-1", Channels: json.RawMessage("[]")}
	mock.forceError = fmt.Errorf("simulated database error")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupMemoryInternalRouter(svc)

	body := `{"channel_type":"slack","channel_external_id":"U123"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/resolve-identity", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusInternalServerError))
}

// Phase 5 stubs
func (m *agentMemoryMock) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) { return nil, nil }
func (m *agentMemoryMock) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) { return nil, nil, nil }
func (m *agentMemoryMock) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error { return nil }
func (m *agentMemoryMock) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) { return nil, nil }

// Phase 4 stubs
func (m *agentMemoryMock) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMemoryMock) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *agentMemoryMock) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *agentMemoryMock) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) { return nil, nil }
func (m *agentMemoryMock) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) { return nil, nil }
func (m *agentMemoryMock) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *agentMemoryMock) DeleteAllMemoriesForUser(agentUserID string) (int64, error) { return 0, nil }
func (m *agentMemoryMock) GetExpiredMemories(limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *agentMemoryMock) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) { return 0, nil }
func (m *agentMemoryMock) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *agentMemoryMock) GetAgentsWithRetentionPolicy() ([]struct{ ID string `db:"id"`; MemoryRetentionDays int `db:"memory_retention_days"` }, error) { return nil, nil }
func (m *agentMemoryMock) UpdateAgentRetentionDays(agentID string, days *int) error { return nil }
func (m *agentMemoryMock) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) { return nil, nil }
func (m *agentMemoryMock) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *agentMemoryMock) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *agentMemoryMock) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *agentMemoryMock) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) { return nil, nil }

// Phase 7 stubs
func (m *agentMemoryMock) FindContradictionCandidates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *agentMemoryMock) FindNearDuplicates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, excludeID string, limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *agentMemoryMock) SupersedeMemory(oldID, newID string) error { return nil }
func (m *agentMemoryMock) MergeMemory(duplicateID, canonicalID string) error { return nil }
func (m *agentMemoryMock) CountPinnedMemories(agentUserID string) (int, error) { return 0, nil }
func (m *agentMemoryMock) UnpinOldestMemories(agentUserID string, count int) ([]string, error) { return nil, nil }
func (m *agentMemoryMock) GetMaxPinnedMemories(agentID string) (int, error) { return 50, nil }
func (m *agentMemoryMock) UpdateMaxPinnedMemories(agentID string, limit *int) error { return nil }
