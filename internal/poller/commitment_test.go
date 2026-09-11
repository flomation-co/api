package poller

import (
	"testing"
	"time"

	api "flomation.app/automate/api"
	"github.com/onsi/gomega"
)

type stubCommitmentPersistence struct {
	agent    *api.Agent
	statuses []string
	created  []api.AgentCommitment
}

func (s *stubCommitmentPersistence) GetDueCommitments(int) ([]*api.AgentCommitment, error) {
	return nil, nil
}

func (s *stubCommitmentPersistence) UpdateCommitmentStatus(_, status string) error {
	s.statuses = append(s.statuses, status)
	return nil
}

func (s *stubCommitmentPersistence) CreateAgentCommitment(c api.AgentCommitment) (*string, error) {
	s.created = append(s.created, c)
	id := "created"
	return &id, nil
}

func (s *stubCommitmentPersistence) GetAgentByID(string) (*api.Agent, error) {
	return s.agent, nil
}

func (s *stubCommitmentPersistence) GetAgentConversationByID(string) (*api.AgentConversation, error) {
	return nil, nil
}

func (s *stubCommitmentPersistence) GetAgentConversationMessages(string, int) ([]*api.AgentMessage, error) {
	return nil, nil
}

type stubDispatcher struct {
	dispatched int
}

func (s *stubDispatcher) DispatchFlow(string, *string, map[string]interface{}) error {
	s.dispatched++
	return nil
}

func runningAgent() *api.Agent {
	flowID := "orchestrator"
	return &api.Agent{Status: api.AgentStatusRunning, OrchestratorFlowID: &flowID}
}

func TestCommitmentDueBeforeItWasMadeIsNotFired(t *testing.T) {
	gomega.RegisterTestingT(t)

	created := time.Date(2026, time.September, 10, 9, 5, 0, 0, time.UTC)
	due := time.Date(2025, time.September, 13, 9, 5, 0, 0, time.UTC)

	p := &stubCommitmentPersistence{agent: runningAgent()}
	d := &stubDispatcher{}
	poller := &CommitmentPoller{persistence: p, dispatcher: d}

	poller.processCommitment(&api.AgentCommitment{
		ID: "one", AgentID: "agent", Description: "Check back", CreatedAt: created, DueAt: &due,
	})

	gomega.Expect(d.dispatched).To(gomega.Equal(0), "a reminder dated before it was made must never be delivered")
	gomega.Expect(p.statuses).To(gomega.Equal([]string{StatusNeedsAttention}))
}

func TestCommitmentDueNowIsFired(t *testing.T) {
	gomega.RegisterTestingT(t)

	created := time.Date(2026, time.September, 10, 9, 5, 0, 0, time.UTC)
	due := created.Add(72 * time.Hour)

	p := &stubCommitmentPersistence{agent: runningAgent()}
	d := &stubDispatcher{}
	poller := &CommitmentPoller{persistence: p, dispatcher: d}

	poller.processCommitment(&api.AgentCommitment{
		ID: "two", AgentID: "agent", Description: "Check back", CreatedAt: created, DueAt: &due,
	})

	gomega.Expect(d.dispatched).To(gomega.Equal(1))
	gomega.Expect(p.statuses).To(gomega.Equal([]string{"firing", "fulfilled"}))
}

func TestCommitmentRecordedSecondsLateStillFires(t *testing.T) {
	gomega.RegisterTestingT(t)

	// Extraction runs after the reply, so a "remind me in 30 seconds"
	// commitment is legitimately written a moment after it fell due.
	created := time.Date(2026, time.September, 10, 9, 5, 0, 0, time.UTC)
	due := created.Add(-30 * time.Second)

	p := &stubCommitmentPersistence{agent: runningAgent()}
	d := &stubDispatcher{}
	poller := &CommitmentPoller{persistence: p, dispatcher: d}

	poller.processCommitment(&api.AgentCommitment{
		ID: "three", AgentID: "agent", Description: "Nudge", CreatedAt: created, DueAt: &due,
	})

	gomega.Expect(d.dispatched).To(gomega.Equal(1))
}

func TestCommitmentWithNoDueDateIsFired(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubCommitmentPersistence{agent: runningAgent()}
	d := &stubDispatcher{}
	poller := &CommitmentPoller{persistence: p, dispatcher: d}

	poller.processCommitment(&api.AgentCommitment{
		ID: "four", AgentID: "agent", Description: "Condition met", CreatedAt: time.Now(),
	})

	gomega.Expect(d.dispatched).To(gomega.Equal(1))
}
