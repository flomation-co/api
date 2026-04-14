package http

// Focused tests for the Phase 2a internal HTTP handlers added in
// agent_memory_phase2.go. Mirrors the Phase 1 test structure
// (agent_memory_test.go): a dedicated mock that records incoming calls
// and returns pre-canned data, plus a minimal router that only wires
// the routes this file exercises.
//
// Each test asserts both the HTTP response AND the exact arguments the
// persistence method was called with — the point isn't just that the
// status code is 200, it's that Launch and the extraction flow will
// reach the DB with the fields they set in their request body.

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
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// phase2Mock extends agentMemoryMock (which in turn extends agentMock)
// so we inherit every Phase 0 and Phase 1 stub and only have to supply
// the Phase 2 methods we care about.
type phase2Mock struct {
	*agentMemoryMock

	p2mu sync.Mutex

	// Recorded calls.
	createMemoryCalls   []api.AgentMemory
	listMemoryCalls     []listMemoryCall
	deleteMemoryCalls   []string
	createPACalls       []api.AgentPendingAction
	listPACalls         []string
	updatePACalls       []updateStatusCall
	createCommitCalls   []api.AgentCommitment
	listDueCommitCalls  []int
	listUserCommitCalls []listUserCommitCall
	updateCommitCalls   []updateStatusCall

	// Pre-canned results.
	memoryResult      *api.AgentMemory
	memoriesResult    []*api.AgentMemory
	pendingResult     *api.AgentPendingAction
	pendingListResult []*api.AgentPendingAction
	commitmentResult  *api.AgentCommitment
	commitmentsDue    []*api.AgentCommitment
	commitmentsUser   []*api.AgentCommitment
	createdID         string
	phase2Error       error
}

type listMemoryCall struct {
	AgentUserID string
	PinnedOnly  bool
	Limit       int
}

type updateStatusCall struct {
	ID     string
	Status string
}

type listUserCommitCall struct {
	AgentUserID string
	Limit       int
}

func newPhase2Mock() *phase2Mock {
	return &phase2Mock{
		agentMemoryMock: newAgentMemoryMock(),
	}
}

// --- CreateAgentMemory ---

func (m *phase2Mock) CreateAgentMemory(mem api.AgentMemory) (*string, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.createMemoryCalls = append(m.createMemoryCalls, mem)
	if m.phase2Error != nil {
		return nil, m.phase2Error
	}
	id := m.createdID
	if id == "" {
		id = "mem-new"
	}
	return &id, nil
}

func (m *phase2Mock) GetAgentMemoryByID(id string) (*api.AgentMemory, error) {
	if m.phase2Error != nil {
		return nil, m.phase2Error
	}
	return m.memoryResult, nil
}

func (m *phase2Mock) GetAgentMemoriesForUser(agentUserID string, pinnedOnly bool, limit int) ([]*api.AgentMemory, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.listMemoryCalls = append(m.listMemoryCalls, listMemoryCall{
		AgentUserID: agentUserID,
		PinnedOnly:  pinnedOnly,
		Limit:       limit,
	})
	if m.phase2Error != nil {
		return nil, m.phase2Error
	}
	return m.memoriesResult, nil
}

func (m *phase2Mock) DeleteAgentMemory(id string) error {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.deleteMemoryCalls = append(m.deleteMemoryCalls, id)
	return m.phase2Error
}

func (m *phase2Mock) TouchAgentMemoryLastUsed(id string) error { return nil }

// --- Pending Action ---

func (m *phase2Mock) CreateAgentPendingAction(pa api.AgentPendingAction) (*string, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.createPACalls = append(m.createPACalls, pa)
	if m.phase2Error != nil {
		return nil, m.phase2Error
	}
	id := m.createdID
	if id == "" {
		id = "pa-new"
	}
	return &id, nil
}

func (m *phase2Mock) GetAgentPendingActionByID(id string) (*api.AgentPendingAction, error) {
	return m.pendingResult, m.phase2Error
}

func (m *phase2Mock) GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.listPACalls = append(m.listPACalls, agentUserID)
	return m.pendingListResult, m.phase2Error
}
func (m *phase2Mock) GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *phase2Mock) MarkPendingActionNotified(id string) error {
	return nil
}

func (m *phase2Mock) UpdatePendingActionStatus(id, status string) error {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.updatePACalls = append(m.updatePACalls, updateStatusCall{ID: id, Status: status})
	return m.phase2Error
}

// --- Commitment ---

func (m *phase2Mock) CreateAgentCommitment(c api.AgentCommitment) (*string, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.createCommitCalls = append(m.createCommitCalls, c)
	if m.phase2Error != nil {
		return nil, m.phase2Error
	}
	id := m.createdID
	if id == "" {
		id = "commit-new"
	}
	return &id, nil
}

func (m *phase2Mock) GetAgentCommitmentByID(id string) (*api.AgentCommitment, error) {
	return m.commitmentResult, m.phase2Error
}

func (m *phase2Mock) GetDueCommitments(limit int) ([]*api.AgentCommitment, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.listDueCommitCalls = append(m.listDueCommitCalls, limit)
	return m.commitmentsDue, m.phase2Error
}

func (m *phase2Mock) GetCommitmentsForUser(agentUserID string, limit int) ([]*api.AgentCommitment, error) {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.listUserCommitCalls = append(m.listUserCommitCalls, listUserCommitCall{
		AgentUserID: agentUserID,
		Limit:       limit,
	})
	return m.commitmentsUser, m.phase2Error
}

func (m *phase2Mock) UpdateCommitmentStatus(id, status string) error {
	m.p2mu.Lock()
	defer m.p2mu.Unlock()
	m.updateCommitCalls = append(m.updateCommitCalls, updateStatusCall{ID: id, Status: status})
	return m.phase2Error
}

func setupPhase2InternalRouter(svc *Service) *gin.Engine {
	router := gin.New()
	internal := router.Group("/internal")
	internal.POST("/agent/:id/memory", svc.createAgentMemoryInternal)
	internal.GET("/agent/:id/memory", svc.listAgentMemoriesInternal)
	internal.GET("/memory/:id", svc.getAgentMemoryInternal)
	internal.DELETE("/memory/:id", svc.deleteAgentMemoryInternal)
	internal.POST("/agent/:id/pending-action", svc.createAgentPendingActionInternal)
	internal.GET("/agent/:id/pending-action", svc.listOpenPendingActionsInternal)
	internal.PATCH("/pending-action/:id", svc.updatePendingActionStatusInternal)
	internal.POST("/agent/:id/commitment", svc.createAgentCommitmentInternal)
	internal.GET("/commitment/due", svc.listDueCommitmentsInternal)
	internal.GET("/agent/:id/commitment", svc.listCommitmentsForUserInternal)
	internal.PATCH("/commitment/:id", svc.updateCommitmentStatusInternal)
	return router
}

// seedAgent puts a single agent record into the mock so GetAgentByID
// returns non-nil. Every handler that takes :id in the URL gates on this.
func seedAgent(m *phase2Mock, agentID string) {
	m.agents[agentID] = &api.Agent{
		ID:       agentID,
		OwnerID:  "user-1",
		Channels: json.RawMessage("[]"),
	}
}

// --- Memory: create ---

func Test_CreateAgentMemoryInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")
	mock.createdID = "mem-42"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	userID := "user-abc"
	body := fmt.Sprintf(`{
		"agent_user_id":"%s",
		"scope":"user",
		"memory_type":"preference",
		"title":"Preferred name",
		"body":"User prefers to be called Andy, not Andrew",
		"confidence":0.95,
		"pinned":true
	}`, userID)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/memory", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var result map[string]string
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["id"]).To(Equal("mem-42"))

	Expect(mock.createMemoryCalls).To(HaveLen(1))
	call := mock.createMemoryCalls[0]
	Expect(call.AgentID).To(Equal("agent-1"))
	Expect(call.AgentUserID).NotTo(BeNil())
	Expect(*call.AgentUserID).To(Equal(userID))
	Expect(call.Scope).To(Equal("user"))
	Expect(call.MemoryType).To(Equal("preference"))
	Expect(call.Title).To(Equal("Preferred name"))
	Expect(call.Body).To(Equal("User prefers to be called Andy, not Andrew"))
	Expect(call.Confidence).To(BeNumerically("~", 0.95, 0.001))
	Expect(call.Pinned).To(BeTrue())
}

func Test_CreateAgentMemoryInternal_ConfidenceDefaultsTo1WhenOmitted(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	// No confidence field → handler should default to 1.0 before calling
	// persistence. This is the contract the agent/remember executor action
	// depends on.
	body := `{"scope":"global","memory_type":"fact","title":"UTC","body":"Operates in UTC"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/memory", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))
	Expect(mock.createMemoryCalls).To(HaveLen(1))
	Expect(mock.createMemoryCalls[0].Confidence).To(BeNumerically("~", 1.0, 0.001))
}

func Test_CreateAgentMemoryInternal_UnknownAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	// No agent seeded.

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	body := `{"scope":"user","memory_type":"fact","title":"x","body":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/ghost/memory", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
	Expect(mock.createMemoryCalls).To(BeEmpty())
}

func Test_CreateAgentMemoryInternal_MissingRequiredField_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	// Missing title (binding:"required")
	body := `{"scope":"user","memory_type":"fact","body":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/memory", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.createMemoryCalls).To(BeEmpty())
}

// --- Memory: list ---

func Test_ListAgentMemoriesInternal_PinnedOnlyRespected(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")
	pinnedID := "user-abc"
	mock.memoriesResult = []*api.AgentMemory{
		{ID: "m1", AgentID: "agent-1", AgentUserID: &pinnedID, Scope: "user", MemoryType: "preference", Title: "Name", Body: "Andy", Pinned: true, Confidence: 1.0},
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/agent/agent-1/memory?agent_user_id=user-abc&pinned=true&limit=20", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []*api.AgentMemory
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(1))
	Expect(result[0].ID).To(Equal("m1"))

	Expect(mock.listMemoryCalls).To(HaveLen(1))
	call := mock.listMemoryCalls[0]
	Expect(call.AgentUserID).To(Equal("user-abc"))
	Expect(call.PinnedOnly).To(BeTrue())
	Expect(call.Limit).To(Equal(20))
}

func Test_ListAgentMemoriesInternal_MissingAgentUserID_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/agent/agent-1/memory", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.listMemoryCalls).To(BeEmpty())
}

func Test_ListAgentMemoriesInternal_ClampLimitToCeiling(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	// limit=999999 should be clamped to the 1000 ceiling.
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/agent-1/memory?agent_user_id=user-abc&limit=999999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.listMemoryCalls).To(HaveLen(1))
	Expect(mock.listMemoryCalls[0].Limit).To(Equal(1000))
}

// --- Memory: delete ---

func Test_DeleteAgentMemoryInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodDelete, "/internal/memory/mem-7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNoContent))
	Expect(mock.deleteMemoryCalls).To(Equal([]string{"mem-7"}))
}

// --- Pending Action: create ---

func Test_CreateAgentPendingActionInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	body := `{
		"agent_user_id":"user-abc",
		"type":"forget_memory",
		"evidence":"forget that I said I like Python",
		"payload":{"memory_id":"mem-42"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/pending-action", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	Expect(mock.createPACalls).To(HaveLen(1))
	call := mock.createPACalls[0]
	Expect(call.AgentID).To(Equal("agent-1"))
	Expect(call.AgentUserID).To(Equal("user-abc"))
	Expect(call.Type).To(Equal("forget_memory"))
	Expect(call.Evidence).To(Equal("forget that I said I like Python"))
	Expect(string(call.Payload)).To(ContainSubstring("mem-42"))
}

// --- Pending Action: list open ---

func Test_ListOpenPendingActionsInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")
	mock.pendingListResult = []*api.AgentPendingAction{
		{ID: "pa-1", AgentID: "agent-1", AgentUserID: "user-abc", Type: "identity_link", Status: "awaiting_confirmation", Evidence: "I'm @andyesser"},
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/agent/agent-1/pending-action?agent_user_id=user-abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []*api.AgentPendingAction
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(1))
	Expect(result[0].ID).To(Equal("pa-1"))

	Expect(mock.listPACalls).To(Equal([]string{"user-abc"}))
}

// --- Pending Action: update status ---

func Test_UpdatePendingActionStatusInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	body := `{"status":"executed"}`
	req := httptest.NewRequest(http.MethodPatch, "/internal/pending-action/pa-1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNoContent))
	Expect(mock.updatePACalls).To(HaveLen(1))
	Expect(mock.updatePACalls[0].ID).To(Equal("pa-1"))
	Expect(mock.updatePACalls[0].Status).To(Equal("executed"))
}

// --- Commitment: create ---

func Test_CreateAgentCommitmentInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	dueAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{
		"agent_user_id":"user-abc",
		"conversation_id":"conv-1",
		"kind":"followup",
		"description":"Compile a shortlist and report back",
		"trigger_type":"time_elapsed",
		"due_at":"%s",
		"made_by":"assistant"
	}`, dueAt)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/commitment", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	Expect(mock.createCommitCalls).To(HaveLen(1))
	call := mock.createCommitCalls[0]
	Expect(call.AgentID).To(Equal("agent-1"))
	Expect(call.Kind).To(Equal("followup"))
	Expect(call.TriggerType).To(Equal("time_elapsed"))
	Expect(call.MadeBy).To(Equal("assistant"))
	Expect(call.ConversationID).NotTo(BeNil())
	Expect(*call.ConversationID).To(Equal("conv-1"))
	Expect(call.DueAt).NotTo(BeNil())
}

// --- Commitment: list due (the Phase 3 poller contract) ---

func Test_ListDueCommitmentsInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	mock.commitmentsDue = []*api.AgentCommitment{
		{ID: "c1", AgentID: "agent-1", Kind: "followup", Description: "Reply to Sarah", TriggerType: "time_elapsed", Status: "pending", MadeBy: "assistant"},
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/commitment/due?limit=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []*api.AgentCommitment
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(1))
	Expect(result[0].ID).To(Equal("c1"))

	Expect(mock.listDueCommitCalls).To(Equal([]int{50}))
}

func Test_ListDueCommitmentsInternal_DefaultLimit(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/internal/commitment/due", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(mock.listDueCommitCalls).To(Equal([]int{100}))
}

// --- Commitment: update status ---

func Test_UpdateCommitmentStatusInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	body := `{"status":"fulfilled"}`
	req := httptest.NewRequest(http.MethodPatch, "/internal/commitment/c1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNoContent))
	Expect(mock.updateCommitCalls).To(HaveLen(1))
	Expect(mock.updateCommitCalls[0].ID).To(Equal("c1"))
	Expect(mock.updateCommitCalls[0].Status).To(Equal("fulfilled"))
}

// --- Error propagation sanity check ---

func Test_CreateAgentMemoryInternal_PersistenceError_Returns500(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2Mock()
	seedAgent(mock, "agent-1")
	mock.phase2Error = fmt.Errorf("simulated database error")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupPhase2InternalRouter(svc)

	body := `{"scope":"user","memory_type":"fact","title":"x","body":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/memory", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusInternalServerError))
}

// Phase 5 stubs
func (m *phase2Mock) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) { return nil, nil }
func (m *phase2Mock) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) { return nil, nil, nil }
func (m *phase2Mock) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error { return nil }
func (m *phase2Mock) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) { return nil, nil }

// Phase 4 stubs
func (m *phase2Mock) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *phase2Mock) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *phase2Mock) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *phase2Mock) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) { return nil, nil }
func (m *phase2Mock) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) { return nil, nil }
func (m *phase2Mock) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *phase2Mock) DeleteAllMemoriesForUser(agentUserID string) (int64, error) { return 0, nil }
func (m *phase2Mock) GetExpiredMemories(limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *phase2Mock) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) { return 0, nil }
func (m *phase2Mock) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *phase2Mock) GetAgentsWithRetentionPolicy() ([]struct{ ID string `db:"id"`; MemoryRetentionDays int `db:"memory_retention_days"` }, error) { return nil, nil }
func (m *phase2Mock) UpdateAgentRetentionDays(agentID string, days *int) error { return nil }
func (m *phase2Mock) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) { return nil, nil }
func (m *phase2Mock) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *phase2Mock) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *phase2Mock) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *phase2Mock) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) { return nil, nil }

// Phase 7 stubs
func (m *phase2Mock) FindContradictionCandidates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *phase2Mock) FindNearDuplicates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, excludeID string, limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *phase2Mock) SupersedeMemory(oldID, newID string) error { return nil }
func (m *phase2Mock) MergeMemory(duplicateID, canonicalID string) error { return nil }
func (m *phase2Mock) CountPinnedMemories(agentUserID string) (int, error) { return 0, nil }
func (m *phase2Mock) UnpinOldestMemories(agentUserID string, count int) ([]string, error) { return nil, nil }
func (m *phase2Mock) GetMaxPinnedMemories(agentID string) (int, error) { return 50, nil }
func (m *phase2Mock) UpdateMaxPinnedMemories(agentID string, limit *int) error { return nil }
