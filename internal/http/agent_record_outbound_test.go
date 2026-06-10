package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// recordAgentOutboundInternal composes the same primitives the inbound
// pipeline uses; these tests exercise the wiring rather than the
// downstream behaviour. Verified properties:
//
//  1. Happy path: identity + conversation + message all get resolved
//     and the response carries channel_scoped=false.
//  2. Channel-scoped path (no recipient_id): identity resolution is
//     skipped, message lands on a conversation with agent_user_id NULL.
//  3. Unknown agent returns 404.
//  4. Personal-mode anonymous-stub rejection (identity returns nil)
//     gracefully degrades to channel-scoped rather than 500.

func setupRecordOutboundRouter(svc *Service) *gin.Engine {
	router := gin.New()
	internal := router.Group("/internal")
	internal.POST("/agent/:id/record-outbound", svc.recordAgentOutboundInternal)
	return router
}

func Test_RecordAgentOutboundInternal_HappyPath(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	orgID := "org-1"
	mock.agents["agent-1"] = &api.Agent{
		ID:                 "agent-1",
		OwnerID:            "user-1",
		OrganisationID:     &orgID,
		IdleTimeoutSeconds: 1800,
		Channels:           json.RawMessage("[]"),
	}
	mock.identityResult = &api.AgentIdentity{
		ID:                "id-1",
		AgentUserID:       "user-bob",
		ChannelType:       "slack",
		ChannelExternalID: "U_BOB",
	}
	mock.userResult = &api.AgentUser{ID: "user-bob", AgentID: "agent-1"}
	mock.conversationResult = &api.AgentConversation{ID: "conv-bob"}
	mock.createMessageID = "msg-1"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupRecordOutboundRouter(svc)

	body := `{"channel_type":"slack","channel_id":"U_BOB","recipient_id":"U_BOB","content":"hi from Andy","source_conversation_id":"conv-andy"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/record-outbound", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var resp struct {
		MessageID      string `json:"message_id"`
		ConversationID string `json:"conversation_id"`
		ChannelScoped  bool   `json:"channel_scoped"`
	}
	Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp.MessageID).To(Equal("msg-1"))
	Expect(resp.ConversationID).To(Equal("conv-bob"))
	Expect(resp.ChannelScoped).To(BeFalse())

	Expect(mock.resolveIdentityCalls).To(HaveLen(1))
	Expect(mock.resolveIdentityCalls[0].ChannelType).To(Equal("slack"))
	Expect(mock.resolveIdentityCalls[0].ChannelExternalID).To(Equal("U_BOB"))

	Expect(mock.resolveConversationCalls).To(HaveLen(1))
	Expect(mock.resolveConversationCalls[0].ChannelID).To(Equal("U_BOB"))
	Expect(mock.resolveConversationCalls[0].AgentUserID).NotTo(BeNil())
	Expect(*mock.resolveConversationCalls[0].AgentUserID).To(Equal("user-bob"))

	Expect(mock.createMessageCalls).To(HaveLen(1))
	saved := mock.createMessageCalls[0].Message
	Expect(saved.Direction).To(Equal("outbound"))
	Expect(saved.Content).To(Equal("hi from Andy"))
	Expect(saved.SourceConversationID).NotTo(BeNil())
	Expect(*saved.SourceConversationID).To(Equal("conv-andy"))
}

func Test_RecordAgentOutboundInternal_ChannelScoped_NoRecipientID(t *testing.T) {
	// Multi-user channel send (e.g. "#general"): no recipient_id is
	// available. Identity resolution must be skipped — calling it
	// with an empty external_id would create garbage stubs. The
	// resulting conversation is channel-scoped (agent_user_id NULL).
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{
		ID:                 "agent-1",
		OwnerID:            "user-1",
		IdleTimeoutSeconds: 1800,
		Channels:           json.RawMessage("[]"),
	}
	mock.conversationResult = &api.AgentConversation{ID: "conv-general"}
	mock.createMessageID = "msg-2"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupRecordOutboundRouter(svc)

	body := `{"channel_type":"slack","channel_id":"C_GENERAL","content":"hi everyone"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/record-outbound", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var resp struct {
		ChannelScoped bool `json:"channel_scoped"`
	}
	Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp.ChannelScoped).To(BeTrue())

	Expect(mock.resolveIdentityCalls).To(BeEmpty())
	Expect(mock.resolveConversationCalls).To(HaveLen(1))
	Expect(mock.resolveConversationCalls[0].AgentUserID).To(BeNil())
}

func Test_RecordAgentOutboundInternal_UnknownAgent_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupRecordOutboundRouter(svc)

	body := `{"channel_type":"slack","channel_id":"X","content":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-ghost/record-outbound", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
	Expect(mock.createMessageCalls).To(BeEmpty())
}

func Test_RecordAgentOutboundInternal_PersonalMode_IdentityReturnsNil_DegradesToChannelScoped(t *testing.T) {
	// In personal mode, ResolveOrCreateAgentIdentity returns (nil, nil)
	// when no declared identity matches, because UpsertAnonymousUser
	// can't run without an organisation (CHECK constraint from
	// migration 83). The handler must NOT 500 — it must fall back to
	// recording the message channel-scoped.
	t.Parallel()
	RegisterTestingT(t)

	mock := newAgentMemoryMock()
	mock.agents["agent-1"] = &api.Agent{
		ID:                 "agent-1",
		OwnerID:            "user-1",
		OrganisationID:     nil, // personal mode
		IdleTimeoutSeconds: 1800,
		Channels:           json.RawMessage("[]"),
	}
	mock.identityResult = nil // resolver returns no match
	mock.userResult = nil
	mock.conversationResult = &api.AgentConversation{ID: "conv-fallback"}
	mock.createMessageID = "msg-fallback"

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupRecordOutboundRouter(svc)

	body := `{"channel_type":"slack","channel_id":"U_BOB","recipient_id":"U_BOB","content":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/agent-1/record-outbound", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var resp struct {
		ChannelScoped bool `json:"channel_scoped"`
	}
	Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
	Expect(resp.ChannelScoped).To(BeTrue(), "identity returned nil → message must land channel-scoped, not 500")

	Expect(mock.resolveConversationCalls).To(HaveLen(1))
	Expect(mock.resolveConversationCalls[0].AgentUserID).To(BeNil())
	Expect(mock.createMessageCalls).To(HaveLen(1))
}
