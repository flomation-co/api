package http

import (
	"testing"

	. "github.com/onsi/gomega"
)

// A flow with no revision (GetLatestRevisionByFloID returns nil,nil) previously
// left the execution stuck in "allocated" after the handler nil-dereferenced
// rev.Data. failUndispatchableExecution must instead terminally fail it.
func TestFailUndispatchableExecution(t *testing.T) {
	RegisterTestingT(t)

	mock := &mockPersistence{}
	svc := &Service{persistence: mock}

	svc.failUndispatchableExecution("exec-1", "flow has no revision")

	// Terminal state: executed + fail, never left at "allocated".
	Expect(mock.executionStatus["exec-1"]).To(Equal("executed"))
	Expect(mock.completionStatus["exec-1"]).To(Equal("fail"))
}
