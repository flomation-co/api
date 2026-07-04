package poller

import (
	"encoding/json"
	"testing"
	"time"

	api "flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

type fakeResumePersistence struct {
	due          []*api.Execution
	timedOutOK   map[string]bool // request_id -> whether MarkHITLTimedOut wins
	markedTimed  []string
	resumed      []string
	resumeData   map[string]json.RawMessage
	checkpointIn json.RawMessage
	savedCP      map[string]json.RawMessage
}

func (f *fakeResumePersistence) GetSuspendedExecutionsForResume(now time.Time, limit int) ([]*api.Execution, error) {
	return f.due, nil
}
func (f *fakeResumePersistence) GetExecutionCheckpoint(id string) (json.RawMessage, error) {
	if f.checkpointIn == nil {
		return json.RawMessage(`{"node_results":{}}`), nil
	}
	return f.checkpointIn, nil
}
func (f *fakeResumePersistence) SaveExecutionCheckpoint(id string, cp interface{}) error {
	if f.savedCP == nil {
		f.savedCP = map[string]json.RawMessage{}
	}
	b, _ := json.Marshal(cp)
	f.savedCP[id] = b
	return nil
}
func (f *fakeResumePersistence) SetExecutionResumeData(id string, data json.RawMessage) error {
	if f.resumeData == nil {
		f.resumeData = map[string]json.RawMessage{}
	}
	f.resumeData[id] = data
	return nil
}
func (f *fakeResumePersistence) UpdateExecutionStatus(id, status string) error {
	if status == "created" {
		f.resumed = append(f.resumed, id)
	}
	return nil
}
func (f *fakeResumePersistence) UpdateCompletionStatus(id, status string) error { return nil }
func (f *fakeResumePersistence) ClearResumeAt(id string) error                   { return nil }
func (f *fakeResumePersistence) MarkHITLTimedOut(requestID string) (bool, error) {
	f.markedTimed = append(f.markedTimed, requestID)
	return f.timedOutOK[requestID], nil
}

type noopNotifier struct{ count int }

func (n *noopNotifier) Notify(tags ...string) { n.count++ }

func strptr(s string) *string { return &s }
func rawptr(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestResumePoller_HITLTimeout_ResumesWithTimeoutOutcome(t *testing.T) {
	g := NewWithT(t)
	f := &fakeResumePersistence{
		due: []*api.Execution{{
			ID:                 "exec-1",
			ResumeTriggerType:  strptr("hitl_response"),
			ResumeTriggerMatch: rawptr(`{"request_id":"req-1"}`),
		}},
		timedOutOK: map[string]bool{"req-1": true},
	}
	rp := &ResumePoller{persistence: f, notifier: &noopNotifier{}}
	rp.poll()

	g.Expect(f.markedTimed).To(ConsistOf("req-1"))
	g.Expect(f.resumed).To(ConsistOf("exec-1"))

	// resume_data must carry outcome=timeout for the Await node to route to
	// its timeout handle.
	var parsed struct {
		Await struct {
			Outcome string `json:"outcome"`
		} `json:"await"`
	}
	g.Expect(json.Unmarshal(f.resumeData["exec-1"], &parsed)).To(Succeed())
	g.Expect(parsed.Await.Outcome).To(Equal("timeout"))
}

func TestResumePoller_HITLAlreadyAnswered_DoesNotResume(t *testing.T) {
	g := NewWithT(t)
	f := &fakeResumePersistence{
		due: []*api.Execution{{
			ID:                 "exec-2",
			ResumeTriggerType:  strptr("hitl_response"),
			ResumeTriggerMatch: rawptr(`{"request_id":"req-2"}`),
		}},
		timedOutOK: map[string]bool{"req-2": false}, // human beat the clock
	}
	rp := &ResumePoller{persistence: f, notifier: &noopNotifier{}}
	rp.poll()

	g.Expect(f.markedTimed).To(ConsistOf("req-2"))
	g.Expect(f.resumed).To(BeEmpty(), "already-answered request must not be resumed by the poller")
}

func TestResumePoller_PlainWait_ResumesWithoutResumeData(t *testing.T) {
	g := NewWithT(t)
	f := &fakeResumePersistence{
		due: []*api.Execution{{ID: "exec-3"}}, // no resume_trigger_type = plain Wait
	}
	rp := &ResumePoller{persistence: f, notifier: &noopNotifier{}}
	rp.poll()

	g.Expect(f.resumed).To(ConsistOf("exec-3"), "plain Wait auto-resume is now activated")
	g.Expect(f.markedTimed).To(BeEmpty())
	g.Expect(f.resumeData).To(BeEmpty(), "plain Wait carries no injected resume data")
}
