package http

import "flomation.app/automate/api"

// Persistence defines the data access methods used by the HTTP handlers.
// This interface is satisfied by *persistence.Service and enables testing
// with mock implementations.
type Persistence interface {
	AddUserToOrganisation(organisationID string, userID string, role ...string) error
	GetOrganisationMembers(organisationID string) ([]*api.OrganisationMember, error)
	RemoveUserFromOrganisation(organisationID string, userID string) error
	GetUserRoleInOrganisation(organisationID string, userID string) (*string, error)
	CreateOrganisationInvite(organisationID string, email *string, role string, createdBy string) (*api.OrganisationInvite, error)
	GetOrganisationInvites(organisationID string) ([]*api.OrganisationInvite, error)
	GetInviteByCode(code string) (*api.OrganisationInvite, error)
	AcceptInvite(inviteID string, acceptedBy string) error
	RevokeInvite(inviteID string, organisationID string) error
	CreateEnvironment(environment api.Environment) (*string, error)
	CreateEnvironmentProperty(environmentID string, environmentKey string, property api.EnvironmentProperty) (*string, error)
	CreateEnvironmentSecret(environmentID string, environmentKey string, secret api.CreateEnvironmentSecret) (*string, error)
	CreateFlo(flo api.Flo) (*string, error)
	CreateFloRevision(revision api.Revision) (*string, error)
	CreateOrganisation(organisation api.Organisation) (*string, error)
	CreateTriggerWithType(trigger api.Trigger) (*string, error)
	CreateUser(user *api.User) (*string, error)
	DeleteEnvironmentByID(ID string) error
	DeleteFlo(flo api.Flo) error
	DeleteTrigger(id string) error
	EnrolRunner(runner api.Runner) (*string, error)
	GetActions() ([]*api.Action, error)
	GetEnvironmentByID(ID string, ownerID string, organisationID *string) (*api.Environment, error)
	GetEnvironmentByName(name string, ownerID string, organisationID *string) (*api.Environment, error)
	GetEnvironmentProperties(environmentID string, environmentKey string) ([]*api.EnvironmentProperty, error)
	GetEnvironmentPropertyByID(environmentID string, environmentKey string, id string) (*api.EnvironmentProperty, error)
	GetEnvironmentPropertyByName(environmentID string, environmentKey string, name string) (*api.EnvironmentProperty, error)
	GetEnvironmentSecretByID(environmentID string, environmentKey string, id string) (*api.EnvironmentSecret, error)
	GetEnvironmentSecretByName(environmentID string, environmentKey string, name string) (*api.EnvironmentSecret, error)
	GetEnvironmentSecrets(environmentID string, environmentKey string) ([]*api.EnvironmentSecret, error)
	GetEnvironments(ownerID string, organisationID *string) ([]*api.Environment, error)
	GetExecutionByID(ID string) (*api.Execution, error)
	GetExecutionForRunnerID(ID string) (*api.Execution, error)
	GetExecutions(offset int64, limit int64, search string, userID string, organisationID *string) ([]*api.Execution, int64, error)
	GetFloByID(floID string) (*api.Flo, error)
	GetLatestRevisionByFloID(ID string) (*api.Revision, error)
	GetMyFlos(userID string, offset int64, limit int64, search string) ([]*api.Flo, int64, error)
	GetMyOrganisations(userID string) ([]*api.Organisation, error)
	GetOrganisationByID(ID string) (*api.Organisation, error)
	GetQueueByRegistrationCode(code string) (*api.Queue, error)
	GetRunnerByID(ID string) (*api.Runner, error)
	GetRunnerByIdentifier(identifier string) (*api.Runner, error)
	GetRunners() ([]*api.Runner, error)
	GetTriggerByID(id string) (*api.Trigger, error)
	GetTriggerInvocationById(id string) (*api.TriggerInvocation, error)
	GetTriggers(ownerID string) ([]*api.Trigger, error)
	GetUsage(ownerID string, organisationID *string) (*api.UserDashboard, error)
	GetUserByID(ID string) (*api.User, error)
	RemoveEnvironmentProperty(propertyID string) error
	RemoveEnvironmentSecret(secretID string) error
	TriggerExecution(floId string, triggerId string, data interface{}) (*string, error)
	UpdateCompletionStatus(ID string, status string) error
	UpdateEnvironmentProperty(environmentID string, environmentKey string, property api.EnvironmentProperty) error
	UpdateExecutionResult(ID string, result interface{}) error
	UpdateExecutionRunnerID(ID string, runnerID string) error
	UpdateExecutionStatus(ID string, status string) error
	UpdateFlo(flo api.Flo) error
	UpdateOrganisation(organisation api.Organisation) error
	UpdateRunnerLastContact(ID string, IPAddress string) error
	UpdateTrigger(trigger api.Trigger) error
	UpdateUser(user *api.User) error
}
