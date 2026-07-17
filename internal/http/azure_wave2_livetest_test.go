package http

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Live tests for the wave-2 option proxies. These drive the REAL handlers —
// not a re-implementation of their signing in the test — against real Azure and
// the local emulators, because that is the only thing that proves a signature
// is right. Wave 1 shipped 80 mocked signing tests and every one of them passed
// while the signer produced a flat 403 against Azurite: a mock validates
// nothing about a signature, since the mock is the thing being asked to accept
// it.
//
// Skipped unless the target's env vars are set:
//
//	source ~/azure_livetest.env && go test ./internal/http/ -run TestLive -v
//
// Plaintext credentials are passed here deliberately. resolveAzureSecretParam
// only reaches the database for a ${secrets.X} reference, so a plain value
// exercises the whole handler without a database or an environment.

func liveCall(t *testing.T, handler func(*gin.Context), params map[string]string) ([]struct{ Name, Value string }, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/?"+q.Encode(), nil)

	handler(c)

	// The option-proxy contract is ALWAYS HTTP 200; failure is carried in the
	// body so the editor can show it inline.
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (the option-proxy contract)", rec.Code)
	}
	var body struct {
		Options []struct{ Name, Value string } `json:"options"`
		Error   string                         `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response did not parse: %v\nbody: %s", err, rec.Body.String())
	}
	return body.Options, body.Error
}

func requireEnv(t *testing.T, keys ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, k := range keys {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s not set — source ~/azure_livetest.env to run this", k)
		}
		out[k] = v
	}
	return out
}

func names(options []struct{ Name, Value string }) []string {
	out := make([]string, 0, len(options))
	for _, o := range options {
		out = append(out, o.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Table Storage
// ---------------------------------------------------------------------------

// TestLiveAzureTablesAgainstAzurite exercises the SharedKeyLite signer against
// the emulator, whose CanonicalizedResource doubles the account
// (/devstoreaccount1/devstoreaccount1/Tables). That doubling is exactly what
// broke wave 1's Blob signer, and it is invisible to a mock.
func TestLiveAzureTablesAgainstAzurite(t *testing.T) {
	env := requireEnv(t, "AZURE_STORAGE_KEY", "AZURE_STORAGE_ENDPOINT")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureTablesTables, map[string]string{
		"account_name": "devstoreaccount1",
		"account_key":  env["AZURE_STORAGE_KEY"],
		"auth_method":  "shared_key",
		"endpoint":     strings.Replace(env["AZURE_STORAGE_ENDPOINT"], ":10000", ":10002", 1),
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	t.Logf("azurite tables: %v", names(options))
}

func TestLiveAzureTablesAgainstRealAzure(t *testing.T) {
	env := requireEnv(t, "AZURE_REAL_STORAGE_ACCOUNT", "AZURE_REAL_STORAGE_KEY")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureTablesTables, map[string]string{
		"account_name": env["AZURE_REAL_STORAGE_ACCOUNT"],
		"account_key":  env["AZURE_REAL_STORAGE_KEY"],
		"auth_method":  "shared_key",
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no tables returned — an empty list proves auth but not parsing; create one first")
	}
	t.Logf("real tables: %v", names(options))
}

// TestLiveAzureTablesRejectsABadKey confirms the failure is reported as a
// credential problem rather than as a parse or network error.
func TestLiveAzureTablesRejectsABadKey(t *testing.T) {
	env := requireEnv(t, "AZURE_REAL_STORAGE_ACCOUNT")
	s := &Service{}

	// Valid base64, wrong key — so it fails at the signature, not at decoding.
	_, errMsg := liveCall(t, s.getAzureTablesTables, map[string]string{
		"account_name": env["AZURE_REAL_STORAGE_ACCOUNT"],
		"account_key":  "MDEyMzQ1Njc4OWFiY2RlZg==",
		"auth_method":  "shared_key",
	})
	if errMsg == "" {
		t.Fatal("a wrong account key was accepted")
	}
	if !strings.Contains(strings.ToLower(errMsg), "unauthoris") && !strings.Contains(strings.ToLower(errMsg), "credential") {
		t.Errorf("error = %q\nwant it to name the credentials", errMsg)
	}
	t.Logf("bad key -> %q", errMsg)
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

// TestLiveAzureFilesShares uses the Blob signer unchanged. Azurite does not
// implement the File service at all, so this only runs against real Azure.
func TestLiveAzureFilesShares(t *testing.T) {
	env := requireEnv(t, "AZURE_REAL_STORAGE_ACCOUNT", "AZURE_REAL_STORAGE_KEY")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureFilesShares, map[string]string{
		"account_name": env["AZURE_REAL_STORAGE_ACCOUNT"],
		"account_key":  env["AZURE_REAL_STORAGE_KEY"],
		"auth_method":  "shared_key",
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no shares returned — create one first, or this proves auth but not parsing")
	}
	t.Logf("shares: %v", names(options))
}

// TestLiveAzureFilesSharesEntraIsRefusedClearly — Entra cannot authorise a
// share list at all, so the proxy must say so instead of surfacing a bare 401.
func TestLiveAzureFilesSharesEntraIsRefusedClearly(t *testing.T) {
	env := requireEnv(t, "AZURE_REAL_STORAGE_ACCOUNT")
	s := &Service{}

	_, errMsg := liveCall(t, s.getAzureFilesShares, map[string]string{
		"account_name": env["AZURE_REAL_STORAGE_ACCOUNT"],
		"auth_method":  "entra",
	})
	if !strings.Contains(errMsg, "Account Key") {
		t.Errorf("error = %q\nwant it to tell the operator to use the Account Key", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Azure DevOps
// ---------------------------------------------------------------------------

func TestLiveAzureDevOpsProjects(t *testing.T) {
	env := requireEnv(t, "AZURE_DEVOPS_ORG_URL", "AZURE_DEVOPS_PAT")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureDevOpsProjects, map[string]string{
		"organisation_url":      env["AZURE_DEVOPS_ORG_URL"],
		"personal_access_token": env["AZURE_DEVOPS_PAT"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no projects returned")
	}
	t.Logf("projects: %v", names(options))
}

func TestLiveAzureDevOpsRepositories(t *testing.T) {
	env := requireEnv(t, "AZURE_DEVOPS_ORG_URL", "AZURE_DEVOPS_PAT", "AZURE_DEVOPS_PROJECT")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureDevOpsRepositories, map[string]string{
		"organisation_url":      env["AZURE_DEVOPS_ORG_URL"],
		"personal_access_token": env["AZURE_DEVOPS_PAT"],
		"project":               env["AZURE_DEVOPS_PROJECT"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no repositories returned")
	}
	t.Logf("repositories: %v", names(options))
}

func TestLiveAzureDevOpsPipelines(t *testing.T) {
	env := requireEnv(t, "AZURE_DEVOPS_ORG_URL", "AZURE_DEVOPS_PAT", "AZURE_DEVOPS_PROJECT")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureDevOpsPipelines, map[string]string{
		"organisation_url":      env["AZURE_DEVOPS_ORG_URL"],
		"personal_access_token": env["AZURE_DEVOPS_PAT"],
		"project":               env["AZURE_DEVOPS_PROJECT"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no pipelines returned")
	}
	// A pipeline is addressed by numeric id, so the VALUE must be the id while
	// the label stays human-readable.
	for _, o := range options {
		if o.Value == "" || strings.ContainsAny(o.Value, "abcdefghijklmnopqrstuvwxyz") {
			t.Errorf("pipeline option %+v: value must be the numeric id", o)
		}
	}
	t.Logf("pipelines: %v", options)
}

func TestLiveAzureDevOpsTeams(t *testing.T) {
	env := requireEnv(t, "AZURE_DEVOPS_ORG_URL", "AZURE_DEVOPS_PAT", "AZURE_DEVOPS_PROJECT")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureDevOpsTeams, map[string]string{
		"organisation_url":      env["AZURE_DEVOPS_ORG_URL"],
		"personal_access_token": env["AZURE_DEVOPS_PAT"],
		"project":               env["AZURE_DEVOPS_PROJECT"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no teams returned")
	}
	t.Logf("teams: %v", names(options))
}

// TestLiveAzureDevOpsBadPATIsACredentialError is the live counterpart to the
// mocked redirect test — it proves the 302-to-sign-in path really is what a bad
// PAT produces, rather than trusting the comment that says so.
func TestLiveAzureDevOpsBadPATIsACredentialError(t *testing.T) {
	env := requireEnv(t, "AZURE_DEVOPS_ORG_URL")
	s := &Service{}

	_, errMsg := liveCall(t, s.getAzureDevOpsProjects, map[string]string{
		"organisation_url":      env["AZURE_DEVOPS_ORG_URL"],
		"personal_access_token": "definitely-not-a-valid-pat",
	})
	if errMsg == "" {
		t.Fatal("an invalid PAT was accepted")
	}
	if !strings.Contains(errMsg, "Personal Access Token") {
		t.Errorf("error = %q\nwant it to name the PAT — an expired token is the failure operators will actually hit, and it must not read as a network problem", errMsg)
	}
	t.Logf("bad PAT -> %q", errMsg)
}

// ---------------------------------------------------------------------------
// Service Bus
// ---------------------------------------------------------------------------

// TestLiveAzureServiceBusEmulatorIsRefusedClearly pins the emulator's hard
// limit. It publishes AMQP only and has no management API, so the list CANNOT
// work — the requirement is that it says so rather than emitting a network
// error the operator would try to debug.
func TestLiveAzureServiceBusEmulatorIsRefusedClearly(t *testing.T) {
	env := requireEnv(t, "AZURE_SERVICEBUS_CONNECTION_STRING")
	if !strings.Contains(strings.ToLower(env["AZURE_SERVICEBUS_CONNECTION_STRING"]), "usedevelopmentemulator") {
		t.Skip("connection string is not the emulator's")
	}
	s := &Service{}

	_, errMsg := liveCall(t, s.getAzureServiceBusQueues, map[string]string{
		"auth_method":       "connection_string",
		"connection_string": env["AZURE_SERVICEBUS_CONNECTION_STRING"],
	})
	if !strings.Contains(errMsg, "emulator") {
		t.Errorf("error = %q\nwant it to name the emulator as the reason", errMsg)
	}
	t.Logf("emulator -> %q", errMsg)
}

// TestLiveAzureServiceBusQueues needs a REAL namespace — set
// AZURE_SERVICEBUS_REAL_CONNECTION_STRING. The emulator cannot serve this.
func TestLiveAzureServiceBusQueues(t *testing.T) {
	env := requireEnv(t, "AZURE_SERVICEBUS_REAL_CONNECTION_STRING")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureServiceBusQueues, map[string]string{
		"auth_method":       "connection_string",
		"connection_string": env["AZURE_SERVICEBUS_REAL_CONNECTION_STRING"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no queues returned")
	}
	t.Logf("queues: %v", names(options))
}

func TestLiveAzureServiceBusTopics(t *testing.T) {
	env := requireEnv(t, "AZURE_SERVICEBUS_REAL_CONNECTION_STRING")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureServiceBusTopics, map[string]string{
		"auth_method":       "connection_string",
		"connection_string": env["AZURE_SERVICEBUS_REAL_CONNECTION_STRING"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no topics returned")
	}
	t.Logf("topics: %v", names(options))
}

func TestLiveAzureServiceBusSubscriptions(t *testing.T) {
	env := requireEnv(t, "AZURE_SERVICEBUS_REAL_CONNECTION_STRING", "AZURE_SERVICEBUS_REAL_TOPIC")
	s := &Service{}

	options, errMsg := liveCall(t, s.getAzureServiceBusSubscriptions, map[string]string{
		"auth_method":       "connection_string",
		"connection_string": env["AZURE_SERVICEBUS_REAL_CONNECTION_STRING"],
		"topic":             env["AZURE_SERVICEBUS_REAL_TOPIC"],
	})
	if errMsg != "" {
		t.Fatalf("proxy returned an error: %s", errMsg)
	}
	if len(options) == 0 {
		t.Fatal("no subscriptions returned")
	}
	t.Logf("subscriptions: %v", names(options))
}
