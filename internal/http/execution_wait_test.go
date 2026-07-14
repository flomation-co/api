package http

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

type waitMock struct {
	mockPersistence
	mu   sync.Mutex
	exec *api.Execution
}

func (m *waitMock) GetExecutionByID(string) (*api.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exec, nil
}
func (m *waitMock) set(e *api.Execution) {
	m.mu.Lock()
	m.exec = e
	m.mu.Unlock()
}

func doWaitRequest(svc *Service) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/execution/:id/wait", svc.getExecutionWaitInternal)
	req := httptest.NewRequest(http.MethodGet, "/execution/e1/wait", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestExecutionWait_ReturnsImmediatelyWhenExecuted(t *testing.T) {
	RegisterTestingT(t)
	mock := &waitMock{exec: &api.Execution{ID: "e1", ExecutionStatus: "executed"}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.completionNotifier = NewExecutionNotifier()

	w := doWaitRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"execution_status":"executed"`))
}

func TestExecutionWait_BlocksThenReturnsOnNotify(t *testing.T) {
	RegisterTestingT(t)
	mock := &waitMock{exec: &api.Execution{ID: "e1", ExecutionStatus: "running"}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.completionNotifier = NewExecutionNotifier()
	orig := executionWaitTimeout
	executionWaitTimeout = 3 * time.Second
	defer func() { executionWaitTimeout = orig }()

	go func() {
		time.Sleep(30 * time.Millisecond)
		mock.set(&api.Execution{ID: "e1", ExecutionStatus: "executed"})
		svc.completionNotifier.Notify("e1")
	}()

	start := time.Now()
	w := doWaitRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"execution_status":"executed"`))
	Expect(time.Since(start)).To(BeNumerically("<", executionWaitTimeout)) // woke via notify, not timeout
}

func TestExecutionWait_TimesOutReturningPending(t *testing.T) {
	RegisterTestingT(t)
	mock := &waitMock{exec: &api.Execution{ID: "e1", ExecutionStatus: "running"}}
	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	svc.completionNotifier = NewExecutionNotifier()
	orig := executionWaitTimeout
	executionWaitTimeout = 40 * time.Millisecond
	defer func() { executionWaitTimeout = orig }()

	w := doWaitRequest(svc)
	Expect(w.Code).To(Equal(http.StatusOK))
	Expect(w.Body.String()).To(ContainSubstring(`"execution_status":"running"`))
}
