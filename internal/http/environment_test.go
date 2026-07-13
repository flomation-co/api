package http

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
	pgvector "github.com/pgvector/pgvector-go"

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

	// Blob store stubs. Keyed by (orgID + hex-handle) so tests can
	// verify cross-org isolation collapses to "not found" without a
	// separate flag. blobQuotaUsed lets blob tests inject a near-cap
	// starting state to exercise the quota-rejection branch.
	blobs         map[string]mockBlob
	blobQuotaUsed map[string]int64
	blobPutErr    error

	// Manual-trigger validation stubs (trigger_inputs feature). When
	// latestRevision is set, GetLatestRevisionByFloID returns it; every
	// TriggerExecution call increments triggerExecCalls so tests can
	// assert an execution was (not) created.
	latestRevision   *api.Revision
	triggerExecCalls int
	triggersByID     map[string]*api.Trigger
}

type mockBlob struct {
	content []byte
	mime    string
	purpose string
}

func newMockPersistence() *mockPersistence {
	return &mockPersistence{
		executions:    make(map[string]*api.Execution),
		flos:          make(map[string]*api.Flo),
		environments:  make(map[string]*api.Environment),
		users:         make(map[string]*api.User),
		properties:    make(map[string]*api.EnvironmentProperty),
		secrets:       make(map[string]*api.EnvironmentSecret),
		blobs:         make(map[string]mockBlob),
		blobQuotaUsed: make(map[string]int64),
	}
}

func (m *mockPersistence) GetExecutionByID(ID string) (*api.Execution, error) {
	return m.executions[ID], nil
}

func (m *mockPersistence) GetRecentPriorConversations(string, string, int) ([]persistence.PriorConversationSummary, error) {
	return nil, nil
}

func (m *mockPersistence) GetConversationMessagesForAgent(string, string, string, int) ([]persistence.PriorConversationMessage, *time.Time, int64, bool, error) {
	return nil, nil, 0, false, nil
}

func (m *mockPersistence) GetAgentUserCalendarAccessToken(string) (string, error) {
	return "", nil
}

// Blob stubs. Tests exercise these through the HTTP handlers in
// blob_test.go. The handle is generated as 16 random bytes so each
// PutBlob produces a unique entry even with identical content.
//
// Cross-org isolation is modelled by keying on orgID + hex-handle.
// A Get/Head/Delete with a different orgID returns the same
// ErrBlobNotFound the real persistence layer does — verified by
// blob_test.go's TestBlob_CrossOrgRead_Returns404.

// blobScopeKey collapses a BlobScope to a string used for the
// in-memory store keying. We deliberately prefix with the scope kind
// so an org and an owner with the same UUID can never collide (and
// so the cross-scope-read-returns-404 invariant holds in tests too).
func blobScopeKey(scope persistence.BlobScope) string {
	if scope.OrgID != "" {
		return "org:" + scope.OrgID
	}
	return "owner:" + scope.OwnerID
}

func (m *mockPersistence) PutBlob(scope persistence.BlobScope, content []byte, mime, purpose string, _ *string) ([]byte, []byte, error) {
	if m.blobPutErr != nil {
		return nil, nil, m.blobPutErr
	}
	if !scope.Valid() {
		return nil, nil, persistence.ErrBlobScopeInvalid
	}
	switch purpose {
	case persistence.BlobPurposeInbound, persistence.BlobPurposeToolOutput, persistence.BlobPurposeManual:
	default:
		return nil, nil, persistence.ErrBlobInvalidPurpose
	}
	quotaKey := blobScopeKey(scope)
	used := m.blobQuotaUsed[quotaKey] + int64(len(content))
	if used > persistence.BlobDailyQuotaPerOrg {
		return nil, nil, persistence.ErrBlobQuotaExceeded
	}
	m.blobQuotaUsed[quotaKey] = used

	handle := make([]byte, persistence.BlobHandleByteLen)
	_, _ = rand.Read(handle)
	digest := sha256.Sum256(content)
	cp := make([]byte, len(content))
	copy(cp, content)
	m.blobs[blobKey(scope, handle)] = mockBlob{content: cp, mime: mime, purpose: purpose}
	return handle, digest[:], nil
}

func (m *mockPersistence) GetBlob(scope persistence.BlobScope, handle []byte) ([]byte, string, int64, error) {
	if !scope.Valid() {
		return nil, "", 0, persistence.ErrBlobScopeInvalid
	}
	b, ok := m.blobs[blobKey(scope, handle)]
	if !ok {
		return nil, "", 0, persistence.ErrBlobNotFound
	}
	return b.content, b.mime, int64(len(b.content)), nil
}

func (m *mockPersistence) HeadBlob(scope persistence.BlobScope, handle []byte) (api.BlobMetadata, error) {
	if !scope.Valid() {
		return api.BlobMetadata{}, persistence.ErrBlobScopeInvalid
	}
	b, ok := m.blobs[blobKey(scope, handle)]
	if !ok {
		return api.BlobMetadata{}, persistence.ErrBlobNotFound
	}
	digest := sha256.Sum256(b.content)
	return api.BlobMetadata{
		HandleHex: hex.EncodeToString(handle),
		Mime:      b.mime,
		SizeBytes: int64(len(b.content)),
		SHA256Hex: hex.EncodeToString(digest[:]),
		Purpose:   b.purpose,
	}, nil
}

func (m *mockPersistence) DeleteBlob(scope persistence.BlobScope, handle []byte) error {
	if !scope.Valid() {
		return persistence.ErrBlobScopeInvalid
	}
	key := blobKey(scope, handle)
	if _, ok := m.blobs[key]; !ok {
		return persistence.ErrBlobNotFound
	}
	delete(m.blobs, key)
	return nil
}

func blobKey(scope persistence.BlobScope, handle []byte) string {
	return blobScopeKey(scope) + ":" + hex.EncodeToString(handle)
}

// Agent Planning M1 stubs. The plan/create handler test in
// agent_plan_test.go shadows these via its own recording mock that
// embeds mockPersistence — the no-op shapes here exist purely so the
// shared mock satisfies the Persistence interface for OTHER test
// files that wire it up for unrelated handlers.

func (m *mockPersistence) VerifyFlowRevision(flowID, revisionID string) (bool, error) {
	return true, nil
}

func (m *mockPersistence) CreatePlanWithTasks(plan *api.Plan, tasks []*api.PlanTask) error {
	return nil
}

func (m *mockPersistence) CreatePlanEvent(event *api.PlanEvent) error {
	return nil
}

func (m *mockPersistence) SetPlanNextCheck(planID string, at time.Time) error {
	return nil
}

func (m *mockPersistence) TickPlan(ctx context.Context, planID string) (*persistence.TickPlanResult, error) {
	return &persistence.TickPlanResult{
		PlanID:     planID,
		PlanStatus: "active",
	}, nil
}

func (m *mockPersistence) HandlePlanTaskCompletion(ctx context.Context, in persistence.PlanTaskCompletionInput) (persistence.WritebackOutcome, error) {
	return persistence.WritebackNone, nil
}

func (m *mockPersistence) BlockPlanTask(ctx context.Context, planTaskID, reason string) (persistence.BlockOutcome, error) {
	return persistence.BlockOutcomeBlocked, nil
}

func (m *mockPersistence) GetPlanByID(id string) (*api.Plan, error) { return nil, nil }
func (m *mockPersistence) GetPlanTasksByPlanID(planID string) ([]*api.PlanTask, error) {
	return nil, nil
}
func (m *mockPersistence) ListPlansByAgentID(agentID, statusFilter string, limit, offset int) ([]*api.Plan, int, error) {
	return nil, 0, nil
}
func (m *mockPersistence) ListPlanEventsByPlanID(planID string, limit int, before *time.Time) ([]*api.PlanEvent, error) {
	return nil, nil
}
func (m *mockPersistence) CancelPlan(ctx context.Context, planID, reason string) (persistence.CancelOutcome, error) {
	return persistence.CancelOutcomeCancelled, nil
}
func (m *mockPersistence) CountPlansCreatedByAgentSince(agentID string, since time.Time) (int, error) {
	return 0, nil
}
func (m *mockPersistence) StartPlan(ctx context.Context, planID string) (persistence.StartOutcome, error) {
	return persistence.StartOutcomeStarted, nil
}
func (m *mockPersistence) RevisePlan(ctx context.Context, planID string, ops persistence.RevisionOps) (persistence.RevisionResult, error) {
	return persistence.RevisionResult{Outcome: persistence.RevisionOutcomeRevised, NewStatus: "active"}, nil
}
func (m *mockPersistence) GetAgentPlanSummary(agentID string) (persistence.PlanSummary, error) {
	return persistence.PlanSummary{}, nil
}

func (m *mockPersistence) GetExecutionTree(rootID string) ([]*api.Execution, error) {
	var out []*api.Execution
	for _, e := range m.executions {
		if e.RootExecutionID == rootID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *mockPersistence) GetExecutionAncestors(id string) ([]*api.Execution, error) {
	exec := m.executions[id]
	if exec == nil {
		return nil, nil
	}
	var chain []*api.Execution
	cursor := exec.ParentExecutionID
	for cursor != nil {
		p := m.executions[*cursor]
		if p == nil {
			break
		}
		chain = append([]*api.Execution{p}, chain...)
		cursor = p.ParentExecutionID
	}
	return chain, nil
}

func (m *mockPersistence) GetExecutionDirectChildren(parentID string) ([]*api.Execution, error) {
	var out []*api.Execution
	for _, e := range m.executions {
		if e.ParentExecutionID != nil && *e.ParentExecutionID == parentID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *mockPersistence) GetFloByID(floID string) (*api.Flo, error) {
	return m.flos[floID], nil
}

func (m *mockPersistence) GetEnvironmentByID(ID string, ownerID string, organisationID *string) (*api.Environment, error) {
	return m.environments[ID], nil
}

func (m *mockPersistence) GetEnvironmentByIDDirect(ID string) (*api.Environment, error) {
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
func (m *mockPersistence) AddUserToOrganisation(string, string, ...string) error {
	panic("not implemented")
}
func (m *mockPersistence) GetOrganisationMembers(string) ([]*api.OrganisationMember, error) {
	panic("not implemented")
}
func (m *mockPersistence) RemoveUserFromOrganisation(string, string) error {
	panic("not implemented")
}
func (m *mockPersistence) GetUserRoleInOrganisation(string, string) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateOrganisationInvite(string, *string, string, string) (*api.OrganisationInvite, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetOrganisationInvites(string) ([]*api.OrganisationInvite, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetInviteByCode(string) (*api.OrganisationInvite, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetInvitePreview(string) (*persistence.InvitePreview, error) {
	panic("not implemented")
}
func (m *mockPersistence) AcceptInvite(string, string) error { panic("not implemented") }
func (m *mockPersistence) RevokeInvite(string, string) error { panic("not implemented") }
func (m *mockPersistence) CreateEnvironment(api.Environment) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateEnvironmentProperty(string, string, api.EnvironmentProperty) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateEnvironmentSecret(string, string, api.CreateEnvironmentSecret) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) CreateFlo(api.Flo) (*string, error)              { panic("not implemented") }
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
func (m *mockPersistence) GetExecutions(int64, int64, string, string, *string, ...bool) ([]*api.Execution, int64, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetLatestRevisionByFloID(string) (*api.Revision, error) {
	return m.latestRevision, nil
}
func (m *mockPersistence) GetMyFlos(string, int64, int64, string, ...string) ([]*api.Flo, int64, error) {
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
func (m *mockPersistence) GetQueuesByOrganisationID(string) ([]*api.Queue, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetQueueByID(string) (*api.Queue, error) { panic("not implemented") }
func (m *mockPersistence) CreateQueue(string, string, *string) (*string, error) {
	panic("not implemented")
}
func (m *mockPersistence) DeleteQueue(string, string) error { panic("not implemented") }
func (m *mockPersistence) GetQueueRunners(string) ([]*api.Runner, error) {
	panic("not implemented")
}
func (m *mockPersistence) AddRunnerToQueue(string, string) error      { panic("not implemented") }
func (m *mockPersistence) RemoveRunnerFromQueue(string, string) error { panic("not implemented") }
func (m *mockPersistence) GetRunnerByID(string) (*api.Runner, error)  { panic("not implemented") }
func (m *mockPersistence) GetRunnerByIdentifier(string) (*api.Runner, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetRunners() ([]*api.Runner, error)          { panic("not implemented") }
func (m *mockPersistence) GetTriggerByID(id string) (*api.Trigger, error) {
	return m.triggersByID[id], nil
}
func (m *mockPersistence) GetTriggerInvocationById(string) (*api.TriggerInvocation, error) {
	panic("not implemented")
}
func (m *mockPersistence) GetTriggers(string) ([]*api.Trigger, error) { panic("not implemented") }
func (m *mockPersistence) GetUsage(string, *string) (*api.UserDashboard, error) {
	panic("not implemented")
}
func (m *mockPersistence) RemoveEnvironmentProperty(string) error { panic("not implemented") }
func (m *mockPersistence) UpdateEnvironmentSecret(string, string, string, string) error {
	panic("not implemented")
}
func (m *mockPersistence) RemoveEnvironmentSecret(string) error { panic("not implemented") }

func (m *mockPersistence) GetCredentialProviders() ([]api.CredentialProvider, error) { return nil, nil }
func (m *mockPersistence) GetCredentialProvider(string) (*api.CredentialProvider, error) {
	return nil, nil
}
func (m *mockPersistence) GetCredentialsByEnvironmentID(string) ([]api.EnvironmentCredential, error) {
	return nil, nil
}
func (m *mockPersistence) GetCredentialByID(string) (*api.EnvironmentCredential, error) {
	return nil, nil
}
func (m *mockPersistence) CreateCredential(*api.EnvironmentCredential, string) (string, error) {
	return "", nil
}
func (m *mockPersistence) StoreCredentialTokens(string, string, string, string, string, string, *time.Time) error {
	return nil
}
func (m *mockPersistence) UpdateCredentialStatus(string, string, *string) error { return nil }
func (m *mockPersistence) DeleteCredential(string, string) error                { return nil }
func (m *mockPersistence) GetDecryptedClientCredentials(string, string) (*string, *string, error) {
	return nil, nil, nil
}
func (m *mockPersistence) GetCredentialByName(string, string, string) (*string, error) {
	return nil, nil
}
func (m *mockPersistence) GetCredentialWithMetaByName(string, string, string) (*string, *json.RawMessage, error) {
	return nil, nil, nil
}
func (m *mockPersistence) UpdateCredentialMetadata(string, *json.RawMessage) error { return nil }
func (m *mockPersistence) GetCredentialsNeedingRefresh(time.Duration) ([]persistence.CredentialRefreshRow, error) {
	return nil, nil
}

func (m *mockPersistence) TriggerExecution(string, string, interface{}, string, *persistence.ParentLink) (*string, error) {
	m.triggerExecCalls++
	id := "exec-1"
	return &id, nil
}
func (m *mockPersistence) IsFlowAgentPaused(string) bool { return false }
func (m *mockPersistence) GetAgentByOrchestratorFloID(string) (*api.Agent, error) {
	return nil, nil
}
func (m *mockPersistence) UpdateCompletionStatus(string, string) error { panic("not implemented") }
func (m *mockPersistence) UpdateEnvironmentProperty(string, string, api.EnvironmentProperty) error {
	panic("not implemented")
}
func (m *mockPersistence) UpdateExecutionResult(string, interface{}) error           { panic("not implemented") }
func (m *mockPersistence) UpdateExecutionRunnerID(string, string) error              { panic("not implemented") }
func (m *mockPersistence) UpdateExecutionStatus(string, string) error                { panic("not implemented") }
func (m *mockPersistence) GetExecutionsBySessionID(string) ([]*api.Execution, error) { return nil, nil }
func (m *mockPersistence) SetExecutionAgentID(string, string) error                  { return nil }
func (m *mockPersistence) SetExecutionAgentSessionID(string, string) error           { return nil }
func (m *mockPersistence) UpdateFlo(api.Flo) error                                   { panic("not implemented") }
func (m *mockPersistence) UpdateOrganisation(api.Organisation) error                 { panic("not implemented") }
func (m *mockPersistence) UpdateRunnerLastContact(string, string) error              { panic("not implemented") }
func (m *mockPersistence) UpdateRunnerVersion(string, *string, *string) error        { return nil }
func (m *mockPersistence) UpdateTrigger(api.Trigger) error                           { panic("not implemented") }
func (m *mockPersistence) GetTriggersByFloID(string) ([]*api.Trigger, error) {
	panic("not implemented")
}
func (m *mockPersistence) LinkFloToTrigger(string, string) error                    { panic("not implemented") }
func (m *mockPersistence) UpdateUser(*api.User) error                               { panic("not implemented") }
func (m *mockPersistence) UpdateUserProfile(*api.User) error                        { panic("not implemented") }
func (m *mockPersistence) AcceptEula(string, int) error                             { return nil }
func (m *mockPersistence) GetLatestEula() (*api.Eula, error)                        { return nil, nil }
func (m *mockPersistence) UpdateOnboardingProgress(string, int, *time.Time) error   { return nil }
func (m *mockPersistence) SetChecklistFlag(string, int) error                       { return nil }
func (m *mockPersistence) ClearChecklistFlag(string, int) error                     { return nil }
func (m *mockPersistence) GetUserChecklistStateForOrg(string, *string) (int, error) { return 0, nil }
func (m *mockPersistence) SetUserChecklistFlagForOrg(string, *string, int) error    { return nil }
func (m *mockPersistence) ClearUserChecklistFlagForOrg(string, *string, int) error  { return nil }
func (m *mockPersistence) CompleteUserWelcome(string, string, bool) error           { return nil }
func (m *mockPersistence) SetUserMarketingOptIn(string, bool) error                 { return nil }
func (m *mockPersistence) MarkUserMarketingSynced(string) error                     { return nil }
func (m *mockPersistence) MarkUserMarketingSyncFailed(string, string) error         { return nil }
func (m *mockPersistence) ListUsersNeedingMarketingSync(int) ([]*api.User, error)   { return nil, nil }

// Favourites stubs
func (m *mockPersistence) GetFloFavourites(string) ([]string, error) { return nil, nil }
func (m *mockPersistence) AddFloFavourite(string, string) error      { return nil }
func (m *mockPersistence) RemoveFloFavourite(string, string) error   { return nil }

// RBAC stubs
func (m *mockPersistence) GetGroupsByOrganisationID(string) ([]*api.Group, error) {
	return nil, nil
}
func (m *mockPersistence) GetGroupByID(string) (*api.Group, error)            { return nil, nil }
func (m *mockPersistence) CreateGroup(api.Group) (*string, error)             { return nil, nil }
func (m *mockPersistence) UpdateGroup(api.Group) error                        { return nil }
func (m *mockPersistence) DeleteGroup(string) error                           { return nil }
func (m *mockPersistence) GetGroupMembers(string) ([]*api.GroupMember, error) { return nil, nil }
func (m *mockPersistence) AddUserToGroup(string, string) error                { return nil }
func (m *mockPersistence) RemoveUserFromGroup(string, string) error           { return nil }
func (m *mockPersistence) SetGroupPermissions(string, []string) error         { return nil }
func (m *mockPersistence) GetUserPermissionsInOrganisation(string, string) ([]string, error) {
	return nil, nil
}
func (m *mockPersistence) GetDefaultGroupsForOrganisation(string) ([]string, error)  { return nil, nil }
func (m *mockPersistence) CountUserGroupsInOrganisation(string, string) (int, error) { return 0, nil }
func (m *mockPersistence) AddAgentToGroup(string, string) error                      { return nil }
func (m *mockPersistence) RemoveAgentFromGroup(string, string) error                 { return nil }
func (m *mockPersistence) GetAgentGroupMembers(string) ([]*api.AgentGroupMember, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentPermissionsInOrganisation(string, string) ([]string, error) {
	return nil, nil
}
func (m *mockPersistence) CountAgentGroupsInOrganisation(string, string) (int, error) { return 0, nil }
func (m *mockPersistence) GetOrganisationAgents(string) ([]*api.OrganisationAgentMember, error) {
	return nil, nil
}
func (m *mockPersistence) CreateFeedback(api.Feedback) error { return nil }

// Agent stubs
func (m *mockPersistence) GetAgents(string) ([]*api.Agent, error)                         { return nil, nil }
func (m *mockPersistence) GetAgentsByOrgID(string) ([]*api.Agent, error)                  { return nil, nil }
func (m *mockPersistence) GetAgentByID(string) (*api.Agent, error)                        { return nil, nil }
func (m *mockPersistence) CreateAgent(api.Agent) (*string, error)                         { return nil, nil }
func (m *mockPersistence) UpdateAgent(api.Agent) error                                    { return nil }
func (m *mockPersistence) ArchiveAgent(string) error                                      { return nil }
func (m *mockPersistence) UpdateAgentStatus(string, string, *time.Time, *time.Time) error { return nil }
func (m *mockPersistence) CreateAgentSession(string) (*string, error)                     { return nil, nil }
func (m *mockPersistence) EndAgentSession(string, string, *string) error                  { return nil }
func (m *mockPersistence) GetAgentSessions(string, int, int) ([]*api.AgentSession, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentSessionByID(string) (*api.AgentSession, error)    { return nil, nil }
func (m *mockPersistence) GetActiveAgentSession(string) (*api.AgentSession, error)  { return nil, nil }
func (m *mockPersistence) GetAgentState(string) ([]*api.AgentState, error)          { return nil, nil }
func (m *mockPersistence) GetAgentStateKey(string, string) (*api.AgentState, error) { return nil, nil }
func (m *mockPersistence) UpsertAgentState(string, string, interface{}) error       { return nil }
func (m *mockPersistence) DeleteAgentStateKey(string, string) error                 { return nil }
func (m *mockPersistence) GetAgentMessages(string, int, int) ([]*api.AgentMessage, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentSessionMessages(string, int, int) ([]*api.AgentMessage, error) {
	return nil, nil
}
func (m *mockPersistence) CreateAgentMessage(api.AgentMessage) (*string, error) { return nil, nil }
func (m *mockPersistence) GetAgentExecutions(string, int, int) ([]*api.AgentExecution, error) {
	return nil, nil
}
func (m *mockPersistence) CreateAgentExecution(api.AgentExecution) (*string, error) { return nil, nil }
func (m *mockPersistence) UpdateAgentExecutionStatus(string, string, *string, *time.Time) error {
	return nil
}
func (m *mockPersistence) CountAgentExecutionsInHour(string) (int64, error) { return 0, nil }

// Agent Memory Phase 1 stubs.
func (m *mockPersistence) ResolveOrCreateAgentIdentity(string, *string, string, string, *string, *string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *mockPersistence) ResolveOrCreateAgentIdentityWithSecondary(string, *string, string, string, *string, *string, *string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *mockPersistence) ResolveOrCreateAgentConversation(string, *string, string, string, *string, int) (*persistence.ConversationResolution, error) {
	return nil, nil
}
func (m *mockPersistence) CloseAgentConversation(string) error {
	return nil
}
func (m *mockPersistence) GetAgentConversationByID(string) (*api.AgentConversation, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentConversationMessages(string, int) ([]*api.AgentMessage, error) {
	return nil, nil
}
func (m *mockPersistence) CreateAgentMessageInConversation(api.AgentMessage) (*string, error) {
	return nil, nil
}

// Agent Memory Phase 2 stubs.
func (m *mockPersistence) CreateAgentMemory(api.AgentMemory) (*string, error)  { return nil, nil }
func (m *mockPersistence) GetAgentMemoryByID(string) (*api.AgentMemory, error) { return nil, nil }
func (m *mockPersistence) GetAgentMemoriesForUser(string, bool, int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteAgentMemory(string) error        { return nil }
func (m *mockPersistence) TouchAgentMemoryLastUsed(string) error { return nil }
func (m *mockPersistence) CreateAgentPendingAction(api.AgentPendingAction) (*string, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentPendingActionByID(string) (*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *mockPersistence) GetOpenPendingActionsForUser(string) ([]*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *mockPersistence) GetUnnotifiedPendingActions(limit int) ([]*api.AgentPendingAction, error) {
	return nil, nil
}
func (m *mockPersistence) MarkPendingActionNotified(id string) error {
	return nil
}
func (m *mockPersistence) UpdatePendingActionStatus(string, string) error { return nil }
func (m *mockPersistence) CreateAgentCommitment(api.AgentCommitment) (*string, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentCommitmentByID(string) (*api.AgentCommitment, error) {
	return nil, nil
}
func (m *mockPersistence) GetDueCommitments(int) ([]*api.AgentCommitment, error) {
	return nil, nil
}
func (m *mockPersistence) GetCommitmentsForUser(string, int) ([]*api.AgentCommitment, error) {
	return nil, nil
}
func (m *mockPersistence) UpdateCommitmentStatus(string, string) error { return nil }

func (m *mockPersistence) UpsertGoogleAccount(string, string, string, string, string) error {
	return nil
}
func (m *mockPersistence) GetGoogleAccounts(string, ...string) ([]*api.AgentUserGoogleAccount, error) {
	return nil, nil
}
func (m *mockPersistence) GetGoogleAccountsForLinkedUsers(string, ...string) ([]*api.AgentUserGoogleAccount, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteGoogleAccount(string, string, ...string) error { return nil }
func (m *mockPersistence) GetGoogleAccountAccessToken(string) (string, error)  { return "", nil }

func (m *mockPersistence) UpsertTriggerGoogleAccount(string, string, string, string, string) error {
	return nil
}
func (m *mockPersistence) GetTriggerGoogleAccounts(string, ...string) ([]*api.TriggerGoogleAccount, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteTriggerGoogleAccount(string, string, ...string) error { return nil }
func (m *mockPersistence) GetTriggerGoogleAccountAccessToken(string) (string, error)  { return "", nil }

// Agent schedule stubs.
func (m *mockPersistence) CreateAgentSchedule(api.AgentSchedule) (*string, error) { return nil, nil }
func (m *mockPersistence) GetAgentSchedules(string) ([]*api.AgentSchedule, error) { return nil, nil }
func (m *mockPersistence) GetAgentSchedulesForUser(string, string) ([]*api.AgentSchedule, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentScheduleByID(string) (*api.AgentSchedule, error) { return nil, nil }
func (m *mockPersistence) UpdateAgentSchedule(api.AgentSchedule) error             { return nil }
func (m *mockPersistence) DeleteAgentSchedule(string) error                        { return nil }
func (m *mockPersistence) DeleteAgentScheduleByName(string, string) error          { return nil }
func (m *mockPersistence) FindAgentScheduleByName(string, string) (*api.AgentSchedule, error) {
	return nil, nil
}
func (m *mockPersistence) GetEnabledAgentSchedules() ([]*api.AgentSchedule, error) { return nil, nil }
func (m *mockPersistence) UpdateAgentScheduleLastFired(string, time.Time) error    { return nil }

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

// Phase 5 stubs
func (m *mockPersistence) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) {
	return nil, nil
}
func (m *mockPersistence) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) {
	return nil, nil, nil
}
func (m *mockPersistence) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error {
	return nil
}
func (m *mockPersistence) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) {
	return nil, nil
}

// Phase 4 stubs
func (m *mockPersistence) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *mockPersistence) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *mockPersistence) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *mockPersistence) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) {
	return nil, nil
}
func (m *mockPersistence) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) {
	return nil, nil
}
func (m *mockPersistence) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *mockPersistence) DeleteAllMemoriesForUser(agentUserID string) (int64, error)  { return 0, nil }
func (m *mockPersistence) GetExpiredMemories(limit int) ([]*api.AgentMemory, error)    { return nil, nil }
func (m *mockPersistence) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) {
	return 0, nil
}
func (m *mockPersistence) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *mockPersistence) GetAgentsWithRetentionPolicy() ([]struct {
	ID                  string `db:"id"`
	MemoryRetentionDays int    `db:"memory_retention_days"`
}, error) {
	return nil, nil
}
func (m *mockPersistence) UpdateAgentRetentionDays(agentID string, days *int) error { return nil }
func (m *mockPersistence) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) {
	return nil, nil
}
func (m *mockPersistence) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	return nil, nil
}
func (m *mockPersistence) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) {
	return nil, nil
}
func (m *mockPersistence) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *mockPersistence) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) {
	return nil, nil
}

// Phase 7 stubs
func (m *mockPersistence) FindContradictionCandidates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *mockPersistence) FindNearDuplicates(agentUserID, memoryType string, embedding pgvector.Vector, threshold float64, excludeID string, limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *mockPersistence) SupersedeMemory(oldID, newID string) error           { return nil }
func (m *mockPersistence) MergeMemory(duplicateID, canonicalID string) error   { return nil }
func (m *mockPersistence) CountPinnedMemories(agentUserID string) (int, error) { return 0, nil }
func (m *mockPersistence) UnpinOldestMemories(agentUserID string, count int) ([]string, error) {
	return nil, nil
}
func (m *mockPersistence) GetMaxPinnedMemories(agentID string) (int, error)         { return 50, nil }
func (m *mockPersistence) UpdateMaxPinnedMemories(agentID string, limit *int) error { return nil }

// Subscription entitlements (billing service integration).
func (m *mockPersistence) UpsertEntitlement(ent *api.SubscriptionEntitlement) error { return nil }
func (m *mockPersistence) GetEntitlement(ownerID string, orgID *string, key string) (*api.SubscriptionEntitlement, error) {
	return nil, nil
}
func (m *mockPersistence) GetAllEntitlements(ownerID string, orgID *string) ([]*api.SubscriptionEntitlement, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteEntitlements(ownerID string, orgID *string) error { return nil }
func (m *mockPersistence) UpsertCreditBalance(ownerID string, orgID *string, balancePence int64) error {
	return nil
}
func (m *mockPersistence) GetCreditBalance(ownerID string, orgID *string) (*api.CreditBalance, error) {
	return nil, nil
}
func (m *mockPersistence) RecordCreditDeduction(deduction *api.CreditDeduction) error { return nil }
func (m *mockPersistence) GetUnsyncedDeductions() ([]*api.CreditDeduction, error)     { return nil, nil }
func (m *mockPersistence) MarkDeductionSynced(id string, amountPence int64) error     { return nil }
func (m *mockPersistence) GetCreditCostsForExecutions(executionIDs []string) (map[string]int64, error) {
	return nil, nil
}
func (m *mockPersistence) TouchUserActivity(userID string)                               {}
func (m *mockPersistence) IncrementSuspendCount(id string) error                         { return nil }
func (m *mockPersistence) AccumulateBillingDuration(id string, additionalMs int64) error { return nil }
func (m *mockPersistence) AppendExecutionSegment(id string, segmentJSON []byte) error    { return nil }
func (m *mockPersistence) ClearResumeAt(id string) error                                 { return nil }
func (m *mockPersistence) SaveExecutionCheckpoint(id string, checkpoint interface{}) error {
	return nil
}
func (m *mockPersistence) SetExecutionResumeAt(id string, resumeAt time.Time) error { return nil }
func (m *mockPersistence) SetExecutionResumeTrigger(id, triggerType string, matchConfig []byte) error {
	return nil
}
func (m *mockPersistence) GetExecutionCheckpoint(id string) (json.RawMessage, error) { return nil, nil }
func (m *mockPersistence) SetExecutionResumeData(id string, data json.RawMessage) error { return nil }
func (m *mockPersistence) GetHITLRequestByExecutionNode(executionID, nodeID string) (*api.HITLRequest, error) {
	return nil, nil
}
func (m *mockPersistence) InsertHITLRequest(req *api.HITLRequest, tokens []api.HITLToken) error {
	return nil
}
func (m *mockPersistence) GetHITLRequestByToken(token string) (*api.HITLRequest, string, error) {
	return nil, "", nil
}
func (m *mockPersistence) GetHITLRequestByID(id string) (*api.HITLRequest, error) { return nil, nil }
func (m *mockPersistence) SetHITLRequestChannels(id string, channels json.RawMessage) error {
	return nil
}
func (m *mockPersistence) ClaimHITLResponse(requestID, option, answeredBy, channel string) (bool, *api.HITLRequest, error) {
	return false, nil, nil
}
func (m *mockPersistence) MarkHITLTimedOut(requestID string) (bool, error) { return false, nil }

// User-declared identities (R2). No-op stubs — tests that exercise the
// identity flow can shadow these on their concrete mock type.
func (m *mockPersistence) CreateUserIdentity(in api.CreateUserIdentity) (*api.UserIdentity, error) {
	return nil, nil
}
func (m *mockPersistence) GetUserIdentitiesByUserID(userID string) ([]*api.UserIdentity, error) {
	return nil, nil
}
func (m *mockPersistence) GetUserIdentitiesByUserAndOrg(userID string, organisationID *string) ([]*api.UserIdentity, error) {
	return nil, nil
}
func (m *mockPersistence) LookupUserIdentityByChannel(organisationID *string, channelType, externalID string) (*api.UserIdentity, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteUserIdentity(userID string, organisationID *string, channelType, externalID string) (int64, error) {
	return 0, nil
}
func (m *mockPersistence) UpsertAnonymousUser(organisationID, channelType, externalID, displayName string) (string, error) {
	return "", nil
}

// Embed-app stubs — the embed handlers get their own focused coverage; these
// satisfy the Persistence interface for the rest of the http test suite.
func (m *mockPersistence) CreateEmbedApp(app *api.EmbedApp, origins []string) (*api.EmbedApp, error) {
	return app, nil
}
func (m *mockPersistence) ListEmbedApps(ownerID string, orgID *string) ([]api.EmbedApp, error) {
	return nil, nil
}
func (m *mockPersistence) GetEmbedApp(id, ownerID string, orgID *string) (*api.EmbedApp, error) {
	return nil, nil
}
func (m *mockPersistence) DeleteEmbedApp(id, ownerID string, orgID *string) (bool, error) {
	return false, nil
}
func (m *mockPersistence) AddEmbedOrigin(appID, origin string) error             { return nil }
func (m *mockPersistence) RemoveEmbedOrigin(appID, origin string) error          { return nil }
func (m *mockPersistence) SetEmbedResource(appID, rt, rid string, en bool) error { return nil }
func (m *mockPersistence) ResolveEmbedKey(pk, origin, rt, rid string) (*api.EmbedResolution, error) {
	return nil, nil
}
