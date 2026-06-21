package poller

// Tests for the plan tick poller. We exercise the `poll()` method
// directly with a stub persistence so the 30-second tick interval
// doesn't dilate every test run. The watch() goroutine is left to
// integration validation (it's a thin time.Ticker wrapper).

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
)

// stubPlanTickPersistence is a recording mock. Each test programs
// the list of plan ids and either a global tick result or per-id
// errors via tickErrByID.
type stubPlanTickPersistence struct {
	planIDs       []string
	listErr       error
	tickErr       error
	tickErrByID   map[string]error
	tickFiredByID map[string]int

	listCalls atomic.Int32
	tickCalls atomic.Int32
}

func (s *stubPlanTickPersistence) ListReadyPlanIDs(limit int) ([]string, error) {
	s.listCalls.Add(1)
	if s.listErr != nil {
		return nil, s.listErr
	}
	if limit < len(s.planIDs) {
		return s.planIDs[:limit], nil
	}
	return s.planIDs, nil
}

func (s *stubPlanTickPersistence) TickPlan(_ context.Context, planID string) (*persistence.TickPlanResult, error) {
	s.tickCalls.Add(1)
	if err, ok := s.tickErrByID[planID]; ok {
		return nil, err
	}
	if s.tickErr != nil {
		return nil, s.tickErr
	}
	fired := s.tickFiredByID[planID]
	result := &persistence.TickPlanResult{
		PlanID:     planID,
		PlanStatus: "active",
	}
	for i := 0; i < fired; i++ {
		result.Fired = append(result.Fired, persistence.FiredTask{
			TaskID: planID + "-task", TaskName: "x",
		})
	}
	return result, nil
}

func TestPoll_EmptyResult_NoTickCalls(t *testing.T) {
	RegisterTestingT(t)
	stub := &stubPlanTickPersistence{}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.listCalls.Load()).To(Equal(int32(1)))
	Expect(stub.tickCalls.Load()).To(Equal(int32(0)))
}

func TestPoll_ListErrorDoesNotPanic(t *testing.T) {
	// A failure listing ready plans is logged and the cycle ends —
	// the next ticker fire tries again. We just want non-panic + no
	// TickPlan calls.
	RegisterTestingT(t)
	stub := &stubPlanTickPersistence{listErr: errors.New("db down")}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.tickCalls.Load()).To(Equal(int32(0)))
}

func TestPoll_TicksEveryReturnedPlan(t *testing.T) {
	RegisterTestingT(t)
	stub := &stubPlanTickPersistence{
		planIDs: []string{"p-1", "p-2", "p-3"},
	}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.tickCalls.Load()).To(Equal(int32(3)))
}

func TestPoll_OneFailDoesntStarveSiblings(t *testing.T) {
	// The poller must not abort the batch on a per-plan failure.
	// p-2 errors; p-1 and p-3 should still be ticked.
	RegisterTestingT(t)
	stub := &stubPlanTickPersistence{
		planIDs:     []string{"p-1", "p-2", "p-3"},
		tickErrByID: map[string]error{"p-2": errors.New("boom")},
	}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.tickCalls.Load()).To(Equal(int32(3)))
}

func TestPoll_TerminalAndLockedAreSwallowed(t *testing.T) {
	// ErrPlanTerminal and ErrPlanLocked aren't warned about — they're
	// expected races. We can't assert log output cleanly without
	// hooking logrus, but we can confirm TickPlan was called for
	// each and the poll completed.
	RegisterTestingT(t)
	stub := &stubPlanTickPersistence{
		planIDs: []string{"p-done", "p-busy", "p-ok"},
		tickErrByID: map[string]error{
			"p-done": persistence.ErrPlanTerminal,
			"p-busy": persistence.ErrPlanLocked,
		},
	}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.tickCalls.Load()).To(Equal(int32(3)))
}

func TestPoll_RespectsBatchSizeImplicit(t *testing.T) {
	// We pass planTickBatchSize into ListReadyPlanIDs; the stub
	// truncates accordingly. Just verifying the cap reaches the
	// stub by adding more ids than the cap and checking only
	// planTickBatchSize tick calls fire.
	RegisterTestingT(t)
	ids := make([]string, planTickBatchSize+10)
	for i := range ids {
		ids[i] = "p"
	}
	stub := &stubPlanTickPersistence{planIDs: ids}
	pp := &PlanTickPoller{persistence: stub}
	pp.poll()
	Expect(stub.tickCalls.Load()).To(Equal(int32(planTickBatchSize)))
}
