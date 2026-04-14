package http

// Tests for Phase 2d-α: the POST /internal/agent/:id/extract handler.
//
// Four branches to cover:
//   1. Unknown agent → 404.
//   2. Agent with nil extraction_flow_id → 204 no-op. This is the
//      "safe to call unconditionally" path that lets Launch and the
//      executor start hitting the endpoint in Phase 2d-γ before the
//      seed migration has populated the column for all agents.
//   3. Happy path → 202 + TriggerExecution called with expected args.
//   4. Malformed / missing body → 400.
//
// Also covers one edge case that matters in production: when the
// extraction flow has no manual trigger. The handler should log and
// fail loudly rather than silently eating the request.

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

// phase2dMock extends agentMemoryMock (so we inherit every Phase 1 and
// Phase 2 method stub) and adds counting/recording behaviour for the
// two persistence methods the extract handler actually calls:
// GetTriggersByFloID and TriggerExecution.
type phase2dMock struct {
	*agentMemoryMock

	p2dmu sync.Mutex

	// Inputs captured from handler calls.
	getTriggersCalls  []string
	triggerExecCalls  []triggerExecCall
	setExecAgentCalls []setExecAgentCall

	// Pre-canned return values.
	triggerList    []*api.Trigger
	executionID    string
	triggerExecErr error
	getTriggersErr error
}

type triggerExecCall struct {
	FloID     string
	TriggerID string
	Data      interface{}
}

type setExecAgentCall struct {
	ExecutionID string
	AgentID     string
}

func newPhase2dMock() *phase2dMock {
	return &phase2dMock{
		agentMemoryMock: newAgentMemoryMock(),
	}
}

func (m *phase2dMock) GetTriggersByFloID(floID string) ([]*api.Trigger, error) {
	m.p2dmu.Lock()
	defer m.p2dmu.Unlock()
	m.getTriggersCalls = append(m.getTriggersCalls, floID)
	if m.getTriggersErr != nil {
		return nil, m.getTriggersErr
	}
	return m.triggerList, nil
}

func (m *phase2dMock) TriggerExecution(floID, triggerID string, data interface{}) (*string, error) {
	m.p2dmu.Lock()
	defer m.p2dmu.Unlock()
	m.triggerExecCalls = append(m.triggerExecCalls, triggerExecCall{
		FloID:     floID,
		TriggerID: triggerID,
		Data:      data,
	})
	if m.triggerExecErr != nil {
		return nil, m.triggerExecErr
	}
	id := m.executionID
	if id == "" {
		id = "exec-new"
	}
	return &id, nil
}

func (m *phase2dMock) SetExecutionAgentID(executionID, agentID string) error {
	m.p2dmu.Lock()
	defer m.p2dmu.Unlock()
	m.setExecAgentCalls = append(m.setExecAgentCalls, setExecAgentCall{
		ExecutionID: executionID,
		AgentID:     agentID,
	})
	return nil
}

func setupPhase2dRouter(svc *Service) *gin.Engine {
	router := gin.New()
	router.POST("/internal/agent/:id/extract", svc.extractAgentInternal)
	return router
}

// seedAgentWithExtractionFlow attaches an agent record to the mock with
// the given extraction_flow_id. Passing nil produces an agent with no
// extraction flow configured (the 204 no-op path).
func seedAgentWithExtractionFlow(m *phase2dMock, agentID string, extractionFlowID *string) {
	m.agents[agentID] = &api.Agent{
		ID:               agentID,
		OwnerID:          "user-1",
		Channels:         json.RawMessage("[]"),
		ExtractionFlowID: extractionFlowID,
	}
}

func strP(s string) *string { return &s }

// --- branch 1: unknown agent ---

func Test_ExtractAgentInternal_UnknownAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	// No agent seeded.

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/ghost/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
	Expect(mock.triggerExecCalls).To(BeEmpty(), "must not dispatch when agent is missing")
}

// --- branch 2: nil extraction_flow_id → 204 no-op ---

func Test_ExtractAgentInternal_NoExtractionFlowConfigured_Returns204(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	// extraction_flow_id nil — the default state for existing agents
	// until the Phase 2d-γ seed migration lands.
	seedAgentWithExtractionFlow(mock, "agent-1", nil)

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNoContent))
	Expect(mock.triggerExecCalls).To(BeEmpty(), "204 path must not dispatch")
	Expect(mock.getTriggersCalls).To(BeEmpty(), "204 path must not look up triggers")
}

func Test_ExtractAgentInternal_EmptyStringExtractionFlowID_Returns204(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	// Empty string is treated the same as nil — a DB row with
	// extraction_flow_id='' shouldn't exist normally but the handler
	// guards against it for safety.
	seedAgentWithExtractionFlow(mock, "agent-1", strP(""))

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNoContent))
}

// --- branch 3: happy path ---

func Test_ExtractAgentInternal_HappyPath_DispatchesFlowWithTriggerData(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-extraction"))
	mock.triggerList = []*api.Trigger{
		{ID: "trigger-1", TypeName: "manual"},
	}
	mock.executionID = "exec-42"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{
		"role": "user",
		"content": "I'm Andy, call me Andy not Andrew",
		"message_id": "msg-123",
		"agent_user_id": "user-abc",
		"conversation_id": "conv-xyz"
	}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusAccepted))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["execution_id"]).To(Equal("exec-42"))

	// Trigger lookup happened on the extraction flow.
	Expect(mock.getTriggersCalls).To(Equal([]string{"flow-extraction"}))

	// TriggerExecution called exactly once with the right flow + trigger.
	Expect(mock.triggerExecCalls).To(HaveLen(1))
	call := mock.triggerExecCalls[0]
	Expect(call.FloID).To(Equal("flow-extraction"))
	Expect(call.TriggerID).To(Equal("trigger-1"))

	// Trigger data should be JSON-encoded with all fields from the request
	// plus agent_id. Decode it back out to assert on the contents.
	raw, ok := call.Data.(json.RawMessage)
	Expect(ok).To(BeTrue(), "trigger data should be json.RawMessage, got %T", call.Data)
	var td map[string]interface{}
	Expect(json.Unmarshal(raw, &td)).To(Succeed())
	Expect(td["agent_id"]).To(Equal("agent-1"))
	Expect(td["role"]).To(Equal("user"))
	Expect(td["content"]).To(Equal("I'm Andy, call me Andy not Andrew"))
	Expect(td["message_id"]).To(Equal("msg-123"))
	Expect(td["agent_user_id"]).To(Equal("user-abc"))
	Expect(td["conversation_id"]).To(Equal("conv-xyz"))

	// Execution was tagged with the agent ID for the admin Executions UI.
	Expect(mock.setExecAgentCalls).To(Equal([]setExecAgentCall{
		{ExecutionID: "exec-42", AgentID: "agent-1"},
	}))
}

func Test_ExtractAgentInternal_OmitsNilOptionalsFromTriggerData(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-extraction"))
	mock.triggerList = []*api.Trigger{{ID: "trigger-1", TypeName: "manual"}}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	// Minimum viable payload — only role and content.
	body := `{"role":"assistant","content":"Hello Andy"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusAccepted))
	Expect(mock.triggerExecCalls).To(HaveLen(1))

	raw, _ := mock.triggerExecCalls[0].Data.(json.RawMessage)
	var td map[string]interface{}
	Expect(json.Unmarshal(raw, &td)).To(Succeed())
	Expect(td["role"]).To(Equal("assistant"))
	Expect(td["content"]).To(Equal("Hello Andy"))
	Expect(td["agent_id"]).To(Equal("agent-1"))

	// The three optional fields must not appear when omitted — otherwise
	// the extraction flow can't distinguish "not provided" from "empty"
	// and the AI node might get literal null strings in its prompt.
	_, hasMsgID := td["message_id"]
	_, hasUserID := td["agent_user_id"]
	_, hasConvID := td["conversation_id"]
	Expect(hasMsgID).To(BeFalse())
	Expect(hasUserID).To(BeFalse())
	Expect(hasConvID).To(BeFalse())
}

// --- edge case: extraction flow has no triggers at all ---

func Test_ExtractAgentInternal_FlowHasNoTriggers_Returns500(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-broken"))
	mock.triggerList = []*api.Trigger{} // no triggers

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusInternalServerError))
	Expect(mock.triggerExecCalls).To(BeEmpty(), "must not dispatch when flow has no triggers")
}

// --- edge case: first trigger is not manual but no manual exists ---

func Test_ExtractAgentInternal_FallsBackToFirstTriggerWhenNoManual(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-extraction"))
	mock.triggerList = []*api.Trigger{
		{ID: "trigger-1", TypeName: "schedule"}, // not manual
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Fall-back to the first trigger should keep the dispatch working
	// even for flows that don't have a manual trigger (e.g. an admin
	// customised the flow to use a different trigger type).
	Expect(w.Code).To(Equal(http.StatusAccepted))
	Expect(mock.triggerExecCalls).To(HaveLen(1))
	Expect(mock.triggerExecCalls[0].TriggerID).To(Equal("trigger-1"))
}

// --- branch 4: malformed / missing body ---

func Test_ExtractAgentInternal_MissingRequiredBodyField_Returns400(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-extraction"))
	mock.triggerList = []*api.Trigger{{ID: "trigger-1", TypeName: "manual"}}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	// Missing role (binding:"required")
	body := `{"content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
	Expect(mock.triggerExecCalls).To(BeEmpty())
}

// --- error propagation ---

func Test_ExtractAgentInternal_TriggerExecutionFails_Returns500(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newPhase2dMock()
	seedAgentWithExtractionFlow(mock, "agent-1", strP("flow-extraction"))
	mock.triggerList = []*api.Trigger{{ID: "trigger-1", TypeName: "manual"}}
	mock.triggerExecErr = fmt.Errorf("queue is full")

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.executionNotifier = NewExecutionNotifier()
	router := setupPhase2dRouter(svc)

	body := `{"role":"user","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/extract", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusInternalServerError))
}

// Phase 5 stubs
func (m *phase2dMock) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) { return nil, nil }
func (m *phase2dMock) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) { return nil, nil, nil }
func (m *phase2dMock) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error { return nil }
func (m *phase2dMock) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) { return nil, nil }

// Phase 4 stubs
func (m *phase2dMock) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *phase2dMock) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *phase2dMock) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *phase2dMock) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) { return nil, nil }
func (m *phase2dMock) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) { return nil, nil }
func (m *phase2dMock) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *phase2dMock) DeleteAllMemoriesForUser(agentUserID string) (int64, error) { return 0, nil }
func (m *phase2dMock) GetExpiredMemories(limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *phase2dMock) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) { return 0, nil }
func (m *phase2dMock) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *phase2dMock) GetAgentsWithRetentionPolicy() ([]struct{ ID string `db:"id"`; MemoryRetentionDays int `db:"memory_retention_days"` }, error) { return nil, nil }
func (m *phase2dMock) UpdateAgentRetentionDays(agentID string, days *int) error { return nil }
func (m *phase2dMock) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) { return nil, nil }
func (m *phase2dMock) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *phase2dMock) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *phase2dMock) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *phase2dMock) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) { return nil, nil }
