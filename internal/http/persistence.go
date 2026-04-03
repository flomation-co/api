package http

import (
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
)

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
	GetInvitePreview(code string) (*persistence.InvitePreview, error)
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
	GetEnvironmentByIDDirect(ID string) (*api.Environment, error)
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
	GetMyFlos(userID string, offset int64, limit int64, search string, organisationID ...string) ([]*api.Flo, int64, error)
	GetMyOrganisations(userID string) ([]*api.Organisation, error)
	GetOrganisationByID(ID string) (*api.Organisation, error)
	GetQueueByRegistrationCode(code string) (*api.Queue, error)
	GetQueuesByOrganisationID(organisationID string) ([]*api.Queue, error)
	GetQueueByID(id string) (*api.Queue, error)
	CreateQueue(organisationID string, name string, parentID *string) (*string, error)
	DeleteQueue(id string, organisationID string) error
	GetQueueRunners(queueID string) ([]*api.Runner, error)
	AddRunnerToQueue(queueID string, runnerID string) error
	RemoveRunnerFromQueue(queueID string, runnerID string) error
	GetRunnerByID(ID string) (*api.Runner, error)
	GetRunnerByIdentifier(identifier string) (*api.Runner, error)
	GetRunners() ([]*api.Runner, error)
	GetTriggerByID(id string) (*api.Trigger, error)
	GetTriggerInvocationById(id string) (*api.TriggerInvocation, error)
	GetTriggers(ownerID string) ([]*api.Trigger, error)
	GetUsage(ownerID string, organisationID *string) (*api.UserDashboard, error)
	GetUserByID(ID string) (*api.User, error)
	RemoveEnvironmentProperty(propertyID string) error
	UpdateEnvironmentSecret(environmentID string, environmentKey string, secretID string, value string) error
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
	GetTriggersByFloID(floID string) ([]*api.Trigger, error)
	LinkFloToTrigger(floID string, triggerID string) error
	UpdateUser(user *api.User) error

	// Favourites
	GetFloFavourites(userID string) ([]string, error)
	AddFloFavourite(userID, floID string) error
	RemoveFloFavourite(userID, floID string) error

	// RBAC
	GetGroupsByOrganisationID(orgID string) ([]*api.Group, error)
	GetGroupByID(groupID string) (*api.Group, error)
	CreateGroup(group api.Group) (*string, error)
	UpdateGroup(group api.Group) error
	DeleteGroup(groupID string) error
	GetGroupMembers(groupID string) ([]*api.GroupMember, error)
	AddUserToGroup(groupID, userID string) error
	RemoveUserFromGroup(groupID, userID string) error
	SetGroupPermissions(groupID string, permissions []string) error
	GetUserPermissionsInOrganisation(orgID, userID string) ([]string, error)
	GetDefaultGroupsForOrganisation(orgID string) ([]string, error)
	CountUserGroupsInOrganisation(orgID, userID string) (int, error)

	// Feedback
	CreateFeedback(feedback api.Feedback) error

	// Agents
	GetAgents(ownerID string) ([]*api.Agent, error)
	GetAgentsByOrgID(organisationID string) ([]*api.Agent, error)
	GetAgentByID(id string) (*api.Agent, error)
	CreateAgent(agent api.Agent) (*string, error)
	UpdateAgent(agent api.Agent) error
	ArchiveAgent(id string) error
	UpdateAgentStatus(id string, status string, startedAt *time.Time, stoppedAt *time.Time) error

	// Agent Sessions
	CreateAgentSession(agentID string) (*string, error)
	EndAgentSession(id string, status string, errorMessage *string) error
	GetAgentSessions(agentID string, limit int, offset int) ([]*api.AgentSession, error)
	GetAgentSessionByID(id string) (*api.AgentSession, error)
	GetActiveAgentSession(agentID string) (*api.AgentSession, error)

	// Agent State
	GetAgentState(agentID string) ([]*api.AgentState, error)
	GetAgentStateKey(agentID string, key string) (*api.AgentState, error)
	UpsertAgentState(agentID string, key string, value interface{}) error
	DeleteAgentStateKey(agentID string, key string) error

	// Agent Messages
	GetAgentMessages(agentID string, limit int, offset int) ([]*api.AgentMessage, error)
	GetAgentSessionMessages(sessionID string, limit int, offset int) ([]*api.AgentMessage, error)
	CreateAgentMessage(msg api.AgentMessage) (*string, error)

	// Agent Executions
	GetAgentExecutions(agentID string, limit int, offset int) ([]*api.AgentExecution, error)
	CreateAgentExecution(exec api.AgentExecution) (*string, error)
	UpdateAgentExecutionStatus(id string, status string, approvedBy *string, completedAt *time.Time) error
	CountAgentExecutionsInHour(agentID string) (int64, error)
}
