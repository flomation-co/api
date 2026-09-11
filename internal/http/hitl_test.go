package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

// hitlMock is a stateful mock for the HITL handler tests. It embeds the shared
// mockPersistence (which satisfies the full interface with no-ops) and overrides
// just the HITL methods with in-memory behaviour.
type hitlMock struct {
	*mockPersistence

	byNode     map[string]*api.HITLRequest // execution_id|node_id -> request
	byID       map[string]*api.HITLRequest
	tokens     map[string]struct{ reqID, opt string }
	claimCalls int
	resumed    []string // execution ids resumed
	checkpoint map[string]json.RawMessage
}

func newHITLMock() *hitlMock {
	return &hitlMock{
		mockPersistence: &mockPersistence{},
		byNode:          map[string]*api.HITLRequest{},
		byID:            map[string]*api.HITLRequest{},
		tokens:          map[string]struct{ reqID, opt string }{},
		checkpoint:      map[string]json.RawMessage{},
	}
}

func (m *hitlMock) GetHITLRequestByExecutionNode(execID, nodeID string) (*api.HITLRequest, error) {
	return m.byNode[execID+"|"+nodeID], nil
}

func (m *hitlMock) InsertHITLRequest(req *api.HITLRequest, tokens []api.HITLToken) error {
	req.ID = "req-" + req.NodeID
	req.Status = "awaiting" // emulate the DB default
	m.byNode[req.ExecutionID+"|"+req.NodeID] = req
	m.byID[req.ID] = req
	for _, t := range tokens {
		m.tokens[t.Token] = struct{ reqID, opt string }{req.ID, t.OptionValue}
	}
	return nil
}

func (m *hitlMock) GetHITLRequestByToken(token string) (*api.HITLRequest, string, error) {
	t, ok := m.tokens[token]
	if !ok {
		return nil, "", nil
	}
	return m.byID[t.reqID], t.opt, nil
}

func (m *hitlMock) GetHITLRequestByID(id string) (*api.HITLRequest, error) { return m.byID[id], nil }

func (m *hitlMock) ClaimHITLResponse(requestID, option, by, channel string) (bool, *api.HITLRequest, error) {
	m.claimCalls++
	r := m.byID[requestID]
	if r == nil || r.Status != "awaiting" {
		return false, nil, nil
	}
	r.Status = "answered"
	r.AnsweredOption = &option
	return true, r, nil
}

func (m *hitlMock) GetExecutionCheckpoint(id string) (json.RawMessage, error) {
	if cp, ok := m.checkpoint[id]; ok {
		return cp, nil
	}
	return json.RawMessage(`{"node_results":{}}`), nil
}
func (m *hitlMock) SaveExecutionCheckpoint(id string, cp interface{}) error {
	// The handler passes pre-marshalled JSON bytes straight to the JSONB
	// column; emulate that (don't re-marshal, which would base64-encode).
	switch v := cp.(type) {
	case []byte:
		m.checkpoint[id] = v
	case json.RawMessage:
		m.checkpoint[id] = v
	default:
		b, _ := json.Marshal(cp)
		m.checkpoint[id] = b
	}
	return nil
}
func (m *hitlMock) UpdateExecutionStatus(id, status string) error {
	if status == "created" {
		m.resumed = append(m.resumed, id)
	}
	return nil
}
func (m *hitlMock) UpdateCompletionStatus(id, status string) error               { return nil }
func (m *hitlMock) ClearResumeAt(id string) error                                { return nil }
func (m *hitlMock) SetExecutionResumeData(id string, data json.RawMessage) error { return nil }

func newHITLService(m *hitlMock) *Service {
	return &Service{persistence: m, logHub: NewLogHub(), executionNotifier: NewExecutionNotifier()}
}

func doJSON(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func TestCreateHITLRequest_MintsTokensAndIsIdempotent(t *testing.T) {
	g := NewWithT(t)
	m := newHITLMock()
	svc := newHITLService(m)
	r := gin.New()
	r.POST("/hitl/request", svc.createHITLRequestInternal)

	body := `{"execution_id":"exec-1","flo_id":"flo-1","node_id":"await-1","message":"Ship?","options":[{"value":"yes","label":"Approve"},{"value":"no","label":"Deny"}],"web_base_url":"https://l.example.com"}`
	rec := doJSON(r, http.MethodPost, "/hitl/request", body)
	g.Expect(rec.Code).To(Equal(http.StatusCreated))

	var resp createHITLRequestResponse
	g.Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
	g.Expect(resp.RequestID).To(Equal("req-await-1"))
	g.Expect(resp.Options).To(HaveLen(2))
	g.Expect(resp.Options[0].Token).NotTo(BeEmpty(), "each option gets a token")
	g.Expect(resp.Options[1].Token).NotTo(Equal(resp.Options[0].Token), "tokens are distinct")

	// Second identical call must return the same request without a new insert.
	rec2 := doJSON(r, http.MethodPost, "/hitl/request", body)
	g.Expect(rec2.Code).To(Equal(http.StatusOK))
	var resp2 createHITLRequestResponse
	g.Expect(json.Unmarshal(rec2.Body.Bytes(), &resp2)).To(Succeed())
	g.Expect(resp2.RequestID).To(Equal("req-await-1"))
}

func TestRespondHITL_FirstResponseWins(t *testing.T) {
	g := NewWithT(t)
	m := newHITLMock()
	svc := newHITLService(m)
	r := gin.New()
	r.POST("/hitl/request", svc.createHITLRequestInternal)
	r.POST("/hitl/respond", svc.respondHITLInternal)

	// Register a request and grab a token.
	create := doJSON(r, http.MethodPost, "/hitl/request",
		`{"execution_id":"exec-9","flo_id":"flo-1","node_id":"await-1","options":[{"value":"yes","label":"Approve"}]}`)
	var reg createHITLRequestResponse
	g.Expect(json.Unmarshal(create.Body.Bytes(), &reg)).To(Succeed())
	token := reg.Options[0].Token

	// First response wins → answered + execution resumed.
	first := doJSON(r, http.MethodPost, "/hitl/respond", `{"token":"`+token+`","answered_by":"U1"}`)
	g.Expect(first.Code).To(Equal(http.StatusOK))
	var fr respondHITLResponse
	g.Expect(json.Unmarshal(first.Body.Bytes(), &fr)).To(Succeed())
	g.Expect(fr.Status).To(Equal("answered"))
	g.Expect(fr.ExecutionID).To(Equal("exec-9"))
	g.Expect(m.resumed).To(ConsistOf("exec-9"), "winning response re-queues the execution exactly once")

	// Second response loses → already_answered, no further resume.
	second := doJSON(r, http.MethodPost, "/hitl/respond", `{"token":"`+token+`","answered_by":"U2"}`)
	g.Expect(second.Code).To(Equal(http.StatusOK))
	var sr respondHITLResponse
	g.Expect(json.Unmarshal(second.Body.Bytes(), &sr)).To(Succeed())
	g.Expect(sr.Status).To(Equal("already_answered"))
	g.Expect(m.resumed).To(HaveLen(1), "losing response must NOT resume again")
}

func TestRespondHITL_PatchesCheckpointWithChosenOption(t *testing.T) {
	g := NewWithT(t)
	m := newHITLMock()
	svc := newHITLService(m)
	r := gin.New()
	r.POST("/hitl/request", svc.createHITLRequestInternal)
	r.POST("/hitl/respond", svc.respondHITLInternal)

	create := doJSON(r, http.MethodPost, "/hitl/request",
		`{"execution_id":"exec-5","flo_id":"flo-1","node_id":"await-1","options":[{"value":"approve","label":"Approve"}]}`)
	var reg createHITLRequestResponse
	g.Expect(json.Unmarshal(create.Body.Bytes(), &reg)).To(Succeed())

	doJSON(r, http.MethodPost, "/hitl/respond", `{"token":"`+reg.Options[0].Token+`","answered_by":"U1"}`)

	// The patched checkpoint must carry the chosen option under resume_data.await.
	cp := m.checkpoint["exec-5"]
	g.Expect(cp).NotTo(BeNil())
	var parsed struct {
		ResumeData struct {
			Await struct {
				Outcome     string `json:"outcome"`
				OptionValue string `json:"option_value"`
			} `json:"await"`
		} `json:"resume_data"`
	}
	g.Expect(json.Unmarshal(cp, &parsed)).To(Succeed())
	g.Expect(parsed.ResumeData.Await.Outcome).To(Equal("option"))
	g.Expect(parsed.ResumeData.Await.OptionValue).To(Equal("approve"))
}
