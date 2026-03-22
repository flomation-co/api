package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"
)

// mockPersistence implements only the methods needed for environment tests.
// All other Persistence interface methods panic if called unexpectedly.
type mockPersistence struct {
	executions   map[string]*api.Execution
	flos         map[string]*api.Flo
	environments map[string]*api.Environment
	users        map[string]*api.User
	properties   map[string]*api.EnvironmentProperty
	secrets      map[string]*api.EnvironmentSecret
}

func newMockPersistence() *mockPersistence {
	return &mockPersistence{
		executions:   make(map[string]*api.Execution),
		flos:         make(map[string]*api.Flo),
		environments: make(map[string]*api.Environment),
		users:        make(map[string]*api.User),
		properties:   make(map[string]*api.EnvironmentProperty),
		secrets:      make(map[string]*api.EnvironmentSecret),
	}
}

func (m *mockPersistence) GetExecutionByID(ID string) (*api.Execution, error) {
	return m.executions[ID], nil
}

func (m *mockPersistence) GetFloByID(floID string) (*api.Flo, error) {
	return m.flos[floID], nil
}

func (m *mockPersistence) GetEnvironmentByID(ID string, ownerID string, organisationID *string) (*api.Environment, error) {
	return m.environments[ID], nil
}

func (m *mockPersistence) GetEnvironmentByName(name string, ownerID string, organisationID *string) (*api.Environment, error) {
	for _, env := range m.environments {
		if env.Name == name {
			return env, nil
		}
	}
	return nil, nil
}

func (m *mockPersistence) GetUserByID(ID string) (*api.User, error) {
	return m.users[ID], nil
}

func (m *mockPersistence) GetEnvironmentPropertyByName(environmentID string, environmentKey string, name string) (*api.EnvironmentProperty, error) {
	key := environmentID + "/" + name
	return m.properties[key], nil
}

func (m *mockPersistence) GetEnvironmentSecretByName(environmentID string, environmentKey string, name string) (*api.EnvironmentSecret, error) {
	key := environmentID + "/" + name
	return m.secrets[key], nil
}

// Unused interface methods — panic to catch unintended calls during tests.
func (m *mockPersistence) AddUserToOrganisation(string, string) error { panic("not implemented") }
func (m *mockPersistence) CreateEnvironment(api.Environment) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateEnvironmentProperty(string, string, api.EnvironmentProperty) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateEnvironmentSecret(string, string, api.CreateEnvironmentSecret) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateFlo(api.Flo) (*string, error)             { panic("not implemented") }
func (m *mockPersistence) CreateFloRevision(api.Revision) (*string, error) { panic("not implemented") }
func (m *mockPersistence) CreateOrganisation(api.Organisation) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateTriggerWithType(api.Trigger) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateUser(*api.User) (*string, error) { panic("not implemented") }
func (m *mockPersistence) DeleteEnvironmentByID(string) error    { panic("not implemented") }
func (m *mockPersistence) DeleteFlo(api.Flo) error               { panic("not implemented") }
func (m *mockPersistence) DeleteTrigger(string) error            { panic("not implemented") }
func (m *mockPersistence) EnrolRunner(api.Runner) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetActions() ([]*api.Action, error) { panic("not implemented") }
func (m *mockPersistence) GetEnvironmentProperties(string, string) ([]*api.EnvironmentProperty, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetEnvironmentPropertyByID(string, string, string) (*api.EnvironmentProperty, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetEnvironmentSecretByID(string, string, string) (*api.EnvironmentSecret, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetEnvironmentSecrets(string, string) ([]*api.EnvironmentSecret, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetEnvironments(string, *string) ([]*api.Environment, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetExecutionForRunnerID(string) (*api.Execution, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetExecutions(int64, int64, string, string, *string) ([]*api.Execution, int64, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetLatestRevisionByFloID(string) (*api.Revision, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetMyFlos(string, int64, int64, string) ([]*api.Flo, int64, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetMyOrganisations(string) ([]*api.Organisation, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetOrganisationByID(string) (*api.Organisation, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetQueueByRegistrationCode(string) (*api.Queue, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetRunnerByID(string) (*api.Runner, error)         { panic("not implemented") }
func (m *mockPersistence) GetRunnerByIdentifier(string) (*api.Runner, error) { panic("not implemented") }
func (m *mockPersistence) GetRunners() ([]*api.Runner, error)                { panic("not implemented") }
func (m *mockPersistence) GetTriggerByID(string) (*api.Trigger, error)       { panic("not implemented") }
func (m *mockPersistence) GetTriggerInvocationById(string) (*api.TriggerInvocation, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetTriggers(string) ([]*api.Trigger, error) { panic("not implemented") }
func (m *mockPersistence) GetUsage(string, *string) (*api.UserDashboard, error) {
	panic("not implemented")
}
func (m *mockPersistence) RemoveEnvironmentProperty(string) error { panic("not implemented") }
func (m *mockPersistence) RemoveEnvironmentSecret(string) error   { panic("not implemented") }
func (m *mockPersistence) TriggerExecution(string, string, interface{}) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) UpdateCompletionStatus(string, string) error { panic("not implemented") }
func (m *mockPersistence) UpdateEnvironmentProperty(string, string, api.EnvironmentProperty) error {
	panic("not implemented")
}
func (m *mockPersistence) UpdateExecutionResult(string, interface{}) error { panic("not implemented") }
func (m *mockPersistence) UpdateExecutionRunnerID(string, string) error    { panic("not implemented") }
func (m *mockPersistence) UpdateExecutionStatus(string, string) error      { panic("not implemented") }
func (m *mockPersistence) UpdateFlo(api.Flo) error                        { panic("not implemented") }
func (m *mockPersistence) UpdateOrganisation(api.Organisation) error       { panic("not implemented") }
func (m *mockPersistence) UpdateRunnerLastContact(string, string) error    { panic("not implemented") }
func (m *mockPersistence) UpdateTrigger(api.Trigger) error                 { panic("not implemented") }
func (m *mockPersistence) UpdateUser(*api.User) error                      { panic("not implemented") }

func setupTestService(mock *mockPersistence) *Service {
	gin.SetMode(gin.TestMode)
	return &Service{
		persistence: mock,
		engine:      gin.New(),
	}
}

func setupTestRouter(svc *Service) *gin.Engine {
	router := gin.New()
	exec := router.Group("/execution")
	exec.GET("/:id/environment/:environment", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.getExecutionEnvironment)
	exec.GET("/:id/environment/:environment/property/:name", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.getExecutionEnvironmentProperty)
	exec.GET("/:id/environment/:environment/secret/:name", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.getExecutionEnvironmentSecret)
	return router
}

func Test_ExecutionEnvironment_MatchingEnvironment_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	envID := "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &envID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+envID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
}

func Test_ExecutionEnvironment_MismatchedEnvironment_Returns403(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	flowEnvID := "00000000-0000-0000-0000-000000000001"
	otherEnvID := "00000000-0000-0000-0000-000000000002"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &flowEnvID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[otherEnvID] = &api.Environment{ID: otherEnvID, Name: "staging", SecretKey: "key456"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+otherEnvID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusForbidden))
}

func Test_ExecutionEnvironment_NoEnvironmentAssigned_Returns403(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: nil}
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/env-any", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusForbidden))
}

func Test_ExecutionEnvironmentProperty_MatchingEnvironment_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	envID := "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &envID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}
	mock.properties[envID+"/db_host"] = &api.EnvironmentProperty{ID: "prop-1", Name: "db_host", Value: "localhost"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+envID+"/property/db_host", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
}

func Test_ExecutionEnvironmentProperty_MismatchedEnvironment_Returns403(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	flowEnvID := "00000000-0000-0000-0000-000000000001"
	otherEnvID := "00000000-0000-0000-0000-000000000002"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &flowEnvID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[otherEnvID] = &api.Environment{ID: otherEnvID, Name: "staging", SecretKey: "key456"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+otherEnvID+"/property/db_host", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusForbidden))
}

func Test_ExecutionEnvironmentSecret_MatchingEnvironment_Returns200(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	envID := "00000000-0000-0000-0000-000000000001"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &envID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[envID] = &api.Environment{ID: envID, Name: "production", SecretKey: "key123"}
	mock.secrets[envID+"/api_key"] = &api.EnvironmentSecret{ID: "sec-1", Name: "api_key", Value: "encrypted-value"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+envID+"/secret/api_key", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))
}

func Test_ExecutionEnvironmentSecret_MismatchedEnvironment_Returns403(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	flowEnvID := "00000000-0000-0000-0000-000000000001"
	otherEnvID := "00000000-0000-0000-0000-000000000002"
	mock := newMockPersistence()
	mock.executions["exec-1"] = &api.Execution{ID: "exec-1", FloID: "flo-1"}
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", EnvironmentID: &flowEnvID}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.environments[otherEnvID] = &api.Environment{ID: otherEnvID, Name: "staging", SecretKey: "key456"}

	svc := setupTestService(mock)
	router := setupTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/execution/exec-1/environment/"+otherEnvID+"/secret/api_key", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusForbidden))
}
