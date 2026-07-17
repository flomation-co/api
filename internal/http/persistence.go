package http

import (
	"context"
	"encoding/json"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	pgvector "github.com/pgvector/pgvector-go"
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
	GetExecutions(offset int64, limit int64, search string, userID string, organisationID *string, rootOnly ...bool) ([]*api.Execution, int64, error)
	GetExecutionTree(rootID string) ([]*api.Execution, error)
	GetExecutionAncestors(id string) ([]*api.Execution, error)
	GetExecutionDirectChildren(parentID string) ([]*api.Execution, error)
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
	CreateUserIdentity(in api.CreateUserIdentity) (*api.UserIdentity, error)
	GetUserIdentitiesByUserID(userID string) ([]*api.UserIdentity, error)
	GetUserIdentitiesByUserAndOrg(userID string, organisationID *string) ([]*api.UserIdentity, error)
	LookupUserIdentityByChannel(organisationID *string, channelType, externalID string) (*api.UserIdentity, error)
	DeleteUserIdentity(userID string, organisationID *string, channelType, externalID string) (int64, error)
	UpsertAnonymousUser(organisationID, channelType, externalID, displayName string) (string, error)
	RemoveEnvironmentProperty(propertyID string) error
	UpdateEnvironmentSecret(environmentID string, environmentKey string, secretID string, value string) error
	RemoveEnvironmentSecret(secretID string) error

	// Credentials
	GetCredentialProviders() ([]api.CredentialProvider, error)
	GetCredentialProvider(slug string) (*api.CredentialProvider, error)
	GetCredentialsByEnvironmentID(environmentID string) ([]api.EnvironmentCredential, error)
	GetCredentialByID(id string) (*api.EnvironmentCredential, error)
	CreateCredential(cred *api.EnvironmentCredential, environmentKey string) (string, error)
	CreateAWSRoleCredential(environmentID, name string, metadata json.RawMessage) (string, error)
	StoreCredentialTokens(id, environmentKey, accessToken, refreshToken, clientID, clientSecret string, expiresAt *time.Time) error
	UpdateCredentialStatus(id, status string, lastError *string) error
	UpdateCredentialMetadata(id string, metadata *json.RawMessage) error
	DeleteCredential(id, environmentID string) error
	GetDecryptedClientCredentials(credentialID, environmentKey string) (*string, *string, error)
	GetCredentialByName(environmentID, name, environmentKey string) (*string, error)
	GetCredentialWithMetaByName(environmentID, name, environmentKey string) (*string, *json.RawMessage, error)
	GetCredentialsNeedingRefresh(within time.Duration) ([]persistence.CredentialRefreshRow, error)

	TriggerExecution(floId string, triggerId string, data interface{}, triggererUserID string, parent *persistence.ParentLink) (*string, error)
	IsFlowAgentPaused(flowID string) bool
	GetAgentByOrchestratorFloID(flowID string) (*api.Agent, error)
	UpdateCompletionStatus(ID string, status string) error
	UpdateEnvironmentProperty(environmentID string, environmentKey string, property api.EnvironmentProperty) error
	UpdateExecutionResult(ID string, result interface{}) error
	SaveExecutionCheckpoint(id string, checkpoint interface{}) error
	GetExecutionCheckpoint(id string) (json.RawMessage, error)
	SetExecutionResumeData(id string, data json.RawMessage) error
	SetExecutionResumeAt(id string, resumeAt time.Time) error
	ClearResumeAt(id string) error
	SetExecutionResumeTrigger(id, triggerType string, matchConfig []byte) error
	IncrementSuspendCount(id string) error
	// Human-in-the-Loop
	GetHITLRequestByExecutionNode(executionID, nodeID string) (*api.HITLRequest, error)
	InsertHITLRequest(req *api.HITLRequest, tokens []api.HITLToken) error
	GetHITLRequestByToken(token string) (*api.HITLRequest, string, error)
	GetHITLRequestByID(id string) (*api.HITLRequest, error)
	SetHITLRequestChannels(id string, channels json.RawMessage) error
	ClaimHITLResponse(requestID, option, answeredBy, channel string) (bool, *api.HITLRequest, error)
	MarkHITLTimedOut(requestID string) (bool, error)
	AccumulateBillingDuration(id string, additionalMs int64) error
	AppendExecutionSegment(id string, segmentJSON []byte) error
	UpdateExecutionRunnerID(ID string, runnerID string) error
	UpdateExecutionStatus(ID string, status string) error
	UpdateFlo(flo api.Flo) error
	UpdateOrganisation(organisation api.Organisation) error
	UpdateRunnerLastContact(ID string, IPAddress string) error
	UpdateRunnerVersion(ID string, version, executorVersion *string) error
	UpdateTrigger(trigger api.Trigger) error
	GetTriggersByFloID(floID string) ([]*api.Trigger, error)
	LinkFloToTrigger(floID string, triggerID string) error
	UpdateUser(user *api.User) error
	UpdateUserProfile(user *api.User) error
	AcceptEula(userID string, version int) error
	GetLatestEula() (*api.Eula, error)
	UpdateOnboardingProgress(userID string, step int, completedAt *time.Time) error
	SetChecklistFlag(userID string, flag int) error
	ClearChecklistFlag(userID string, flag int) error
	GetUserChecklistStateForOrg(userID string, organisationID *string) (int, error)
	SetUserChecklistFlagForOrg(userID string, organisationID *string, flag int) error
	ClearUserChecklistFlagForOrg(userID string, organisationID *string, flag int) error
	CompleteUserWelcome(userID, name string, marketingOptIn bool) error
	SetUserMarketingOptIn(userID string, optIn bool) error
	MarkUserMarketingSynced(userID string) error
	MarkUserMarketingSyncFailed(userID, reason string) error
	ListUsersNeedingMarketingSync(limit int) ([]*api.User, error)

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

	// Agent RBAC
	AddAgentToGroup(groupID, agentID string) error
	RemoveAgentFromGroup(groupID, agentID string) error
	GetAgentGroupMembers(groupID string) ([]*api.AgentGroupMember, error)
	GetAgentPermissionsInOrganisation(orgID, agentID string) ([]string, error)
	CountAgentGroupsInOrganisation(orgID, agentID string) (int, error)
	GetOrganisationAgents(orgID string) ([]*api.OrganisationAgentMember, error)

	// Feedback
	CreateFeedback(feedback api.Feedback) error

	GetExecutionsBySessionID(sessionID string) ([]*api.Execution, error)
	SetExecutionAgentID(executionID string, agentID string) error
	SetExecutionAgentSessionID(executionID string, sessionID string) error

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

	// Agent Memory Phase 1: identity + conversation scoping. See
	// plans/agent_memory.md and internal/persistence/agent_memory.go.
	ResolveOrCreateAgentIdentity(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string) (*api.AgentIdentity, *api.AgentUser, error)
	ResolveOrCreateAgentIdentityWithSecondary(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string, secondaryExternalID *string) (*api.AgentIdentity, *api.AgentUser, error)
	GetAgentConversationByID(id string) (*api.AgentConversation, error)
	ResolveOrCreateAgentConversation(agentID string, agentUserID *string, channelType, channelID string, threadID *string, idleTimeout int) (*persistence.ConversationResolution, error)
	CloseAgentConversation(conversationID string) error
	GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error)
	GetRecentPriorConversations(agentID, agentUserID string, limit int) ([]persistence.PriorConversationSummary, error)
	GetConversationMessagesForAgent(conversationID, agentID, agentUserID string, maxMessages int) ([]persistence.PriorConversationMessage, *time.Time, int64, bool, error)
	GetAgentUserCalendarAccessToken(agentUserID string) (string, error)
	CreateAgentMessageInConversation(msg api.AgentMessage) (*string, error)

	// Blob store service — file storage tier shared between the
	// executor (tool-output tokenisation) and Launch (inbound
	// attachment dispatch). The BlobScope argument is the
	// discriminated-union auth boundary (org XOR owner). See
	// plans/file_attachments.md and internal/persistence/blob_object.go.
	PutBlob(scope persistence.BlobScope, content []byte, mime, purpose string, execID *string) ([]byte, []byte, error)
	GetBlob(scope persistence.BlobScope, handle []byte) ([]byte, string, int64, error)
	HeadBlob(scope persistence.BlobScope, handle []byte) (api.BlobMetadata, error)
	DeleteBlob(scope persistence.BlobScope, handle []byte) error

	// Agent Planning M1 — see plans/agent_planning_m1.md.
	VerifyFlowRevision(flowID, revisionID string) (bool, error)
	CreatePlanWithTasks(plan *api.Plan, tasks []*api.PlanTask) error
	CreatePlanEvent(event *api.PlanEvent) error
	SetPlanNextCheck(planID string, at time.Time) error
	TickPlan(ctx context.Context, planID string) (*persistence.TickPlanResult, error)
	HandlePlanTaskCompletion(ctx context.Context, in persistence.PlanTaskCompletionInput) (persistence.WritebackOutcome, error)

	// Agent Planning M1.5 — AI-initiated block of an in-flight task.
	BlockPlanTask(ctx context.Context, planTaskID, reason string) (persistence.BlockOutcome, error)

	// Agent Planning M2 — editor-facing read endpoints.
	GetPlanByID(id string) (*api.Plan, error)
	GetPlanTasksByPlanID(planID string) ([]*api.PlanTask, error)
	ListPlansByAgentID(agentID, statusFilter string, limit, offset int) ([]*api.Plan, int, error)
	ListPlanEventsByPlanID(planID string, limit int, before *time.Time) ([]*api.PlanEvent, error)

	// Agent Planning M3 — cancel surface (user + AI).
	CancelPlan(ctx context.Context, planID, reason string) (persistence.CancelOutcome, error)

	// Agent Planning M3.5 — per-agent rate cap on plan/create.
	CountPlansCreatedByAgentSince(agentID string, since time.Time) (int, error)

	// Agent Planning M4 — draft → active transition.
	StartPlan(ctx context.Context, planID string) (persistence.StartOutcome, error)

	// Agent Planning M5 — modify a plan's task graph (add / remove
	// / update). Atomic, validated against the projected post-revise
	// graph. Auto-transitions blocked → active when all failed
	// tasks are removed.
	RevisePlan(ctx context.Context, planID string, ops persistence.RevisionOps) (persistence.RevisionResult, error)

	// Agent Planning M6 — in-flight plan summary for the system
	// prompt's PLAN STATUS block.
	GetAgentPlanSummary(agentID string) (persistence.PlanSummary, error)

	// Agent Memory Phase 2: memories, pending actions, commitments. See
	// plans/agent_memory.md and internal/persistence/agent_memory_phase2.go.
	CreateAgentMemory(mem api.AgentMemory) (*string, error)
	GetAgentMemoryByID(id string) (*api.AgentMemory, error)
	GetAgentMemoriesForUser(agentUserID string, pinnedOnly bool, limit int) ([]*api.AgentMemory, error)
	DeleteAgentMemory(id string) error
	TouchAgentMemoryLastUsed(id string) error
	CreateAgentPendingAction(pa api.AgentPendingAction) (*string, error)
	GetAgentPendingActionByID(id string) (*api.AgentPendingAction, error)
	GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error)
	GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error)
	MarkPendingActionNotified(id string) error
	UpdatePendingActionStatus(id, status string) error
	CreateAgentCommitment(c api.AgentCommitment) (*string, error)
	GetAgentCommitmentByID(id string) (*api.AgentCommitment, error)
	GetDueCommitments(limit int) ([]*api.AgentCommitment, error)
	GetCommitmentsForUser(agentUserID string, limit int) ([]*api.AgentCommitment, error)
	UpdateCommitmentStatus(id, status string) error

	// Agent Memory Phase 4: semantic retrieval with pgvector.
	SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error)
	GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error)
	UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error

	// Agent Memory Phase 5: identity linking.
	GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error)
	LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error)
	MergeAgentUsers(agentID, sourceUserID, targetUserID string) error
	GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error)

	// Agent Memory Phase 6: user-visible memory management + retention + audit.
	GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error)
	GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error)
	UpdateAgentMemory(id, title, body string, pinned bool) error
	DeleteAllMemoriesForUser(agentUserID string) (int64, error)
	GetExpiredMemories(limit int) ([]*api.AgentMemory, error)
	DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error)
	DeleteExpiredMemories(limit int) (int64, error)
	GetAgentsWithRetentionPolicy() ([]struct {
		ID                  string `db:"id"`
		MemoryRetentionDays int    `db:"memory_retention_days"`
	}, error)
	UpdateAgentRetentionDays(agentID string, days *int) error
	CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error)
	GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error)
	GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error)
	UnlinkAgentIdentity(identityID string) error
	GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error)

	// Agent Memory Phase 7: memory hygiene.
	FindContradictionCandidates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, limit int) ([]*api.AgentMemory, error)
	FindNearDuplicates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, excludeID string, limit int) ([]*api.AgentMemory, error)
	SupersedeMemory(oldID, newID string) error
	MergeMemory(duplicateID, canonicalID string) error
	CountPinnedMemories(agentUserID string) (int, error)
	UnpinOldestMemories(agentUserID string, count int) ([]string, error)
	GetMaxPinnedMemories(agentID string) (int, error)
	UpdateMaxPinnedMemories(agentID string, limit *int) error

	// Google account connections (Calendar, Gmail, etc.) — agent-user scoped
	UpsertGoogleAccount(agentUserID, email, refreshToken, label, purpose string) error
	GetGoogleAccounts(agentUserID string, purpose ...string) ([]*api.AgentUserGoogleAccount, error)
	// GetGoogleAccountsForLinkedUsers widens the lookup to include any
	// agent_user that represents the same Flomation user via
	// user_identity declarations. See persistence.go for the CTE walk.
	GetGoogleAccountsForLinkedUsers(agentUserID string, purpose ...string) ([]*api.AgentUserGoogleAccount, error)
	DeleteGoogleAccount(agentUserID, email string, purpose ...string) error
	GetGoogleAccountAccessToken(id string) (string, error)

	// Google account connections — trigger scoped (standalone flows)
	UpsertTriggerGoogleAccount(triggerID, email, refreshToken, label, purpose string) error
	GetTriggerGoogleAccounts(triggerID string, purpose ...string) ([]*api.TriggerGoogleAccount, error)
	DeleteTriggerGoogleAccount(triggerID, email string, purpose ...string) error
	GetTriggerGoogleAccountAccessToken(id string) (string, error)

	// Agent schedules — AI-managed recurring flow execution.
	CreateAgentSchedule(s api.AgentSchedule) (*string, error)
	GetAgentSchedules(agentID string) ([]*api.AgentSchedule, error)
	GetAgentSchedulesForUser(agentID, agentUserID string) ([]*api.AgentSchedule, error)
	GetAgentScheduleByID(id string) (*api.AgentSchedule, error)
	UpdateAgentSchedule(s api.AgentSchedule) error
	DeleteAgentSchedule(id string) error
	DeleteAgentScheduleByName(agentID, name string) error
	FindAgentScheduleByName(agentID, name string) (*api.AgentSchedule, error)
	GetEnabledAgentSchedules() ([]*api.AgentSchedule, error)
	UpdateAgentScheduleLastFired(id string, firedAt time.Time) error

	// Subscription entitlements (pushed from billing service).
	UpsertEntitlement(ent *api.SubscriptionEntitlement) error
	GetEntitlement(ownerID string, orgID *string, key string) (*api.SubscriptionEntitlement, error)
	GetAllEntitlements(ownerID string, orgID *string) ([]*api.SubscriptionEntitlement, error)
	DeleteEntitlements(ownerID string, orgID *string) error

	// Credit balance (pushed from billing service).
	UpsertCreditBalance(ownerID string, orgID *string, balancePence int64) error
	GetCreditBalance(ownerID string, orgID *string) (*api.CreditBalance, error)
	RecordCreditDeduction(deduction *api.CreditDeduction) error
	GetUnsyncedDeductions() ([]*api.CreditDeduction, error)
	MarkDeductionSynced(id string, amountPence int64) error
	GetCreditCostsForExecutions(executionIDs []string) (map[string]int64, error)

	// User activity tracking.
	TouchUserActivity(userID string)

	// Embed apps — developer SDK control plane.
	CreateEmbedApp(app *api.EmbedApp, origins []string) (*api.EmbedApp, error)
	ListEmbedApps(ownerID string, orgID *string) ([]api.EmbedApp, error)
	GetEmbedApp(id, ownerID string, orgID *string) (*api.EmbedApp, error)
	DeleteEmbedApp(id, ownerID string, orgID *string) (bool, error)
	AddEmbedOrigin(appID, origin string) error
	RemoveEmbedOrigin(appID, origin string) error
	SetEmbedResource(appID, resourceType, resourceID string, enabled bool) error
	ResolveEmbedKey(publishableKey, origin, resourceType, resourceID string) (*api.EmbedResolution, error)

	// Flomation Gateway — developer-defined HTTP APIs that route to flows.
	CreateGatewayAPI(a *api.GatewayAPI) (*api.GatewayAPI, error)
	ListGatewayAPIs(ownerID string, orgID *string) ([]api.GatewayAPI, error)
	GetGatewayAPI(id, ownerID string, orgID *string) (*api.GatewayAPI, error)
	UpdateGatewayAPI(a *api.GatewayAPI, ownerID string, orgID *string, secretHash, secretSalt *string) (bool, error)
	DeleteGatewayAPI(id, ownerID string, orgID *string) (bool, error)
	CreateGatewayEndpoint(e *api.GatewayEndpoint) (*api.GatewayEndpoint, error)
	UpdateGatewayEndpoint(e *api.GatewayEndpoint) (bool, error)
	DeleteGatewayEndpoint(id, gatewayAPIID string) (bool, error)
	ResolveGatewayAPI(apiID string) (*api.GatewayResolution, error)

	// Web threads — generic web-invoke conversation history.
	CreateWebThread(flowID string, userID *string) (string, error)
	GetWebThreadHistory(threadID string, limit int) ([]persistence.WebThreadTurn, error)
	AppendWebThreadTurn(threadID, role, content string) error
}
