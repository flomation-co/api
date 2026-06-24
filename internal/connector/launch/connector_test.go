package launch

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/gomega"

	"flomation.app/automate/api/internal/config"
)

// Test_RegisterTrigger_SkipsInternalTypes pins the M3 fix that
// stops the API forwarding internal-only triggers (currently just
// 'plan-task') to the Launch service. Launch's `triggertype` enum
// doesn't include these so the registration would 400 — but more
// importantly, fired-by-API-side triggers shouldn't enter Launch's
// polling model at all.
func Test_RegisterTrigger_SkipsInternalTypes(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}
	connector := NewConnector(cfg)

	err := connector.RegisterTrigger("trigger-1", "plan-task", nil, "flow-1", "")
	Expect(err).To(BeNil())
	Expect(called).To(BeFalse(), "Launch must NOT be contacted for internal trigger types")
}

func Test_RegisterTrigger_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Expect(r.Method).To(Equal(http.MethodPost))
		Expect(r.URL.Path).To(Equal("/trigger/test-trigger-id"))
		Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))

		b, err := io.ReadAll(r.Body)
		Expect(err).To(BeNil())
		defer func() {
			_ = r.Body.Close()
		}()

		err = json.Unmarshal(b, &receivedBody)
		Expect(err).To(BeNil())

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	data := []byte(`{"cron":"* * * * *"}`)
	err := connector.RegisterTrigger("test-trigger-id", "schedule", data, "test-flow-id", "")
	Expect(err).To(BeNil())

	Expect(receivedBody["id"]).To(Equal("test-trigger-id"))
	Expect(receivedBody["type"]).To(Equal("schedule"))
	Expect(receivedBody["flow_id"]).To(Equal("test-flow-id"))
}

func Test_RegisterTrigger_NilData(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		Expect(err).To(BeNil())
		defer func() {
			_ = r.Body.Close()
		}()

		err = json.Unmarshal(b, &receivedBody)
		Expect(err).To(BeNil())

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.RegisterTrigger("id-1", "manual", nil, "flow-1", "")
	Expect(err).To(BeNil())

	// When data is nil, should send empty object
	Expect(receivedBody["data"]).To(Not(BeNil()))
}

func Test_RegisterTrigger_ServerError(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.RegisterTrigger("id-1", "manual", nil, "flow-1", "")
	Expect(err).To(Not(BeNil()))
}

func Test_RegisterTrigger_Unreachable(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: "http://localhost:1"},
	}

	connector := NewConnector(cfg)

	err := connector.RegisterTrigger("id-1", "manual", nil, "flow-1", "")
	Expect(err).To(Not(BeNil()))
}

func Test_DisableTrigger_Success(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Expect(r.Method).To(Equal(http.MethodDelete))
		Expect(r.URL.Path).To(Equal("/trigger/trigger-to-delete"))

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.DisableTrigger("trigger-to-delete", "")
	Expect(err).To(BeNil())
}

func Test_DisableTrigger_ServerError(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.DisableTrigger("trigger-to-delete", "")
	Expect(err).To(Not(BeNil()))
}

func Test_DisableTrigger_NotFound(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.DisableTrigger("nonexistent", "")
	Expect(err).To(Not(BeNil()))
}

func Test_DisableTrigger_Unreachable(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: "http://localhost:1"},
	}

	connector := NewConnector(cfg)

	err := connector.DisableTrigger("id-1", "")
	Expect(err).To(Not(BeNil()))
}

func Test_RegisterTrigger_ForwardsAuthToken(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Expect(r.Header.Get("Authorization")).To(Equal("Bearer my-jwt-token"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.RegisterTrigger("id-1", "manual", nil, "flow-1", "my-jwt-token")
	Expect(err).To(BeNil())
}

func Test_DisableTrigger_ForwardsAuthToken(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Expect(r.Header.Get("Authorization")).To(Equal("Bearer my-jwt-token"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{
		Launch: config.LaunchConfig{URL: server.URL},
	}

	connector := NewConnector(cfg)

	err := connector.DisableTrigger("trigger-1", "my-jwt-token")
	Expect(err).To(BeNil())
}
