package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

type webThreadMock struct {
	mockPersistence
	createdFlow  string
	createdUser  *string
	createReturn string
	history      []persistence.WebThreadTurn
	appended     []persistence.WebThreadTurn
}

func (m *webThreadMock) CreateWebThread(flowID string, userID *string) (string, error) {
	m.createdFlow, m.createdUser = flowID, userID
	return m.createReturn, nil
}
func (m *webThreadMock) GetWebThreadHistory(threadID string, limit int) ([]persistence.WebThreadTurn, error) {
	return m.history, nil
}
func (m *webThreadMock) AppendWebThreadTurn(threadID, role, content string) error {
	m.appended = append(m.appended, persistence.WebThreadTurn{Role: role, Content: content})
	return nil
}

func webThreadRouter(svc *Service) *gin.Engine {
	r := gin.New()
	internal := r.Group("/internal")
	internal.POST("/web-thread", svc.createWebThreadInternal)
	internal.GET("/web-thread/:id/history", svc.getWebThreadHistoryInternal)
	internal.POST("/web-thread/:id/turn", svc.appendWebThreadTurnInternal)
	return r
}

func doReq(r *gin.Engine, method, target, body, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebThread_CreateBindsForwardedUser(t *testing.T) {
	RegisterTestingT(t)
	orig := resolveUserFromToken
	defer func() { resolveUserFromToken = orig }()
	resolveUserFromToken = func(_ string, token string) (string, error) {
		if token == "good" {
			return "user-9", nil
		}
		return "", errors.New("bad")
	}

	mock := &webThreadMock{createReturn: "thread-1"}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.config = &config.Config{Security: config.SecurityConfig{IdentityService: "http://id"}}

	w := doReq(webThreadRouter(svc), http.MethodPost, "/internal/web-thread", `{"flow_id":"flow-1"}`, "Bearer good")

	Expect(w.Code).To(Equal(http.StatusCreated))
	Expect(w.Body.String()).To(ContainSubstring("thread-1"))
	Expect(mock.createdFlow).To(Equal("flow-1"))
	Expect(mock.createdUser).To(Not(BeNil()))
	Expect(*mock.createdUser).To(Equal("user-9"))
}

func TestWebThread_CreateAnonymousWhenNoToken(t *testing.T) {
	RegisterTestingT(t)
	mock := &webThreadMock{createReturn: "thread-2"}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := doReq(webThreadRouter(svc), http.MethodPost, "/internal/web-thread", `{"flow_id":"flow-1"}`, "")

	Expect(w.Code).To(Equal(http.StatusCreated))
	Expect(mock.createdUser).To(BeNil()) // anonymous
}

func TestWebThread_History(t *testing.T) {
	RegisterTestingT(t)
	mock := &webThreadMock{history: []persistence.WebThreadTurn{
		{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"},
	}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock

	w := doReq(webThreadRouter(svc), http.MethodGet, "/internal/web-thread/t1/history?limit=5", "", "")

	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"content":"hi"`))
	Expect(w.Body.String()).To(ContainSubstring(`"content":"hello"`))
}

func TestWebThread_AppendValidatesRole(t *testing.T) {
	RegisterTestingT(t)
	mock := &webThreadMock{}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	r := webThreadRouter(svc)

	ok := doReq(r, http.MethodPost, "/internal/web-thread/t1/turn", `{"role":"assistant","content":"hey"}`, "")
	Expect(ok.Code).To(Equal(http.StatusCreated))
	Expect(mock.appended).To(HaveLen(1))
	Expect(mock.appended[0]).To(Equal(persistence.WebThreadTurn{Role: "assistant", Content: "hey"}))

	bad := doReq(r, http.MethodPost, "/internal/web-thread/t1/turn", `{"role":"system","content":"x"}`, "")
	Expect(bad.Code).To(Equal(http.StatusBadRequest))
}
