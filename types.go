package api

import (
	"encoding/json"
	"time"

	"github.com/lib/pq"
	pgvector "github.com/pgvector/pgvector-go"
)

const (
	ActionTypeTrigger     = 1
	ActionTypeAction      = 2
	ActionTypeOutput      = 3
	ActionTypeConditional = 4
	ActionTypeLoop        = 5
	ActionTypeSwitch      = 6
)

type Organisation struct {
	ID                 string     `json:"id" db:"id"`
	Name               string     `json:"name" db:"name"`
	Icon               *string    `json:"icon,omitempty" db:"icon"`
	Role               string     `json:"role,omitempty" db:"role"`
	AllowPublicRunners bool       `json:"allow_public_runners" db:"allow_public_runners"`
	CreatedAt          *time.Time `json:"created_at" db:"created_at"`
}

type OrganisationMember struct {
	UserID       string     `json:"user_id" db:"user_id"`
	Name         string     `json:"name" db:"name"`
	EmailAddress *string    `json:"email_address,omitempty" db:"email_address"`
	Role         string     `json:"role" db:"role"`
	JoinedAt     *time.Time `json:"joined_at" db:"joined_at"`
}

type OrganisationInvite struct {
	ID             string     `json:"id" db:"id"`
	OrganisationID string     `json:"organisation_id" db:"organisation_id"`
	Email          *string    `json:"email,omitempty" db:"email"`
	InviteCode     string     `json:"invite_code" db:"invite_code"`
	Role           string     `json:"role" db:"role"`
	CreatedBy      string     `json:"created_by" db:"created_by"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty" db:"accepted_at"`
	AcceptedBy     *string    `json:"accepted_by,omitempty" db:"accepted_by"`
	ExpiresAt      time.Time  `json:"expires_at" db:"expires_at"`
}

type User struct {
	ID                    string     `json:"id" db:"id"`
	Name                  string     `json:"name" db:"name"`
	EmailAddress          *string    `json:"email_address" db:"email_address"`
	MarketingOptIn        bool       `json:"marketing_opt_in" db:"marketing_opt_in"`
	EulaVersion           int        `json:"eula_version" db:"eula_version"`
	EulaAcceptedAt        *time.Time `json:"eula_accepted_at,omitempty" db:"eula_accepted_at"`
	OnboardingStep        int        `json:"onboarding_step" db:"onboarding_step"`
	OnboardingCompletedAt *time.Time `json:"onboarding_completed_at,omitempty" db:"onboarding_completed_at"`
	ChecklistFlags        int        `json:"checklist_flags" db:"checklist_flags"`
	LastActivityAt        *time.Time `json:"last_activity_at,omitempty" db:"last_activity_at"`
	// WelcomeCompletedAt records the user dismissing the post-EULA
	// welcome modal (display name + optional marketing opt-in). NULL =
	// modal still due. The editor mounts the modal until this is set.
	WelcomeCompletedAt *time.Time `json:"welcome_completed_at,omitempty" db:"welcome_completed_at"`
	// MarketingSyncedAt / MarketingSyncError track EmailOctopus sync
	// state. The retry poller reads marketing_sync_error to find rows
	// that need re-sync; clears it on success and stamps synced_at.
	MarketingSyncedAt  *time.Time `json:"-" db:"marketing_synced_at"`
	MarketingSyncError *string    `json:"-" db:"marketing_sync_error"`
	// Extended profile fields surfaced in flows as ${user.X} variables.
	// All nullable — empty/NULL collapses to "" at substitution time.
	Salutation    *string        `json:"salutation,omitempty" db:"salutation"`
	FirstName     *string        `json:"first_name,omitempty" db:"first_name"`
	LastName      *string        `json:"last_name,omitempty" db:"last_name"`
	JobTitle      *string        `json:"job_title,omitempty" db:"job_title"`
	AddressLine1  *string        `json:"address_line_1,omitempty" db:"address_line_1"`
	AddressLine2  *string        `json:"address_line_2,omitempty" db:"address_line_2"`
	City          *string        `json:"city,omitempty" db:"city"`
	Region        *string        `json:"region,omitempty" db:"region"`
	Postcode      *string        `json:"postcode,omitempty" db:"postcode"`
	Country       *string        `json:"country,omitempty" db:"country"`
	CreatedAt     time.Time      `json:"created_at" db:"created_at"`
	Organisations []Organisation `json:"organisations"`
}

type Eula struct {
	ID        int       `json:"id" db:"id"`
	Version   int       `json:"version" db:"version"`
	Content   string    `json:"content" db:"content"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type Trigger struct {
	ID             string      `json:"id" db:"id"`
	Name           string      `json:"name" db:"name"`
	OwnerID        *string     `json:"owner_id" db:"owner_id"`
	OrganisationID *string     `json:"organisation_id" db:"organisation_id"`
	CreatedAt      time.Time   `json:"created_at" db:"created_at"`
	Type           string      `json:"type" db:"type"`
	TypeName       string      `json:"type_name" db:"type_name"`
	Data           interface{} `json:"data,omitempty" db:"data"`
	FloID          *string     `json:"flo_id,omitempty" db:"flo_id"`
}

type TriggerInvocation struct {
	ID             string          `json:"id" db:"id"`
	TriggerID      string          `json:"trigger_id" db:"trigger_id"`
	OwnerID        *string         `json:"owner_id" db:"owner_id"`
	OrganisationID *string         `json:"organisation_id" db:"organisation_id"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	Data           json.RawMessage `json:"data" db:"data"`
}

type Flo struct {
	ID                      string            `json:"id" db:"id"`
	Name                    string            `json:"name" db:"name"`
	OrganisationID          *string           `json:"organisation_id,omitempty" db:"organisation_id"`
	AuthorID                *string           `json:"author_id,omitempty" db:"author_id"`
	CreatedAt               *time.Time        `json:"created_at" db:"created_at"`
	LatestRevision          *Revision         `json:"revision,omitempty"`
	Scale                   float32           `json:"scale" db:"scale"`
	XPosition               float32           `json:"x" db:"x"`
	YPosition               float32           `json:"y" db:"y"`
	Triggers                []*Trigger        `json:"triggers"`
	ExecutionCount          int64             `json:"execution_count" db:"execution_count"`
	LastRun                 *string           `json:"last_run" db:"last_run"`
	Duration                *int64            `json:"duration" db:"duration"`
	DurationAdditional      *int64            `json:"duration_additional" db:"duration_additional"`
	LastExecution           *Execution        `json:"last_execution" db:"last_execution"`
	EnvironmentID           *string           `json:"environment_id" db:"environment_id"`
	EnvironmentName         *string           `json:"environment_name" db:"environment_name"`
	QueueID                 *string           `json:"queue_id" db:"queue_id"`
	HasValidationErrors     bool              `json:"has_validation_errors,omitempty"`
	RecentExecutions        []ExecutionStatus `json:"recent_executions,omitempty"`
	NotifyOnSuccess         bool              `json:"notify_on_success" db:"notify_on_success"`
	NotifyOnFailure         bool              `json:"notify_on_failure" db:"notify_on_failure"`
	NotificationEmails      *string           `json:"notification_emails,omitempty" db:"notification_emails"`
	SystemPrompt            *string           `json:"system_prompt,omitempty" db:"system_prompt"`
	SystemFlow              bool              `json:"system_flow" db:"system_flow"`
	SystemFlowPurpose       *string           `json:"system_flow_purpose,omitempty" db:"system_flow_purpose"`
	MaxConcurrentExecutions *int              `json:"max_concurrent_executions,omitempty" db:"max_concurrent_executions"`
}

type ExecutionStatus struct {
	ID               string `json:"id" db:"id"`
	ExecutionStatus  string `json:"execution_status" db:"execution_status"`
	CompletionStatus string `json:"completion_status" db:"completion_status"`
}

type Execution struct {
	ID                 string           `json:"id" db:"id"`
	FloID              string           `json:"flo_id" db:"flo_id"`
	Name               string           `json:"name" db:"name"`
	OwnerID            string           `json:"owner_id" db:"owner_id"`
	OrganisationID     *string          `json:"organisation_id" db:"organisation_id"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt          *time.Time       `json:"updated_at" db:"updated_at"`
	CompletedAt        *time.Time       `json:"completed_at" db:"completed_at"`
	TriggeredBy        *string          `json:"triggered_by" db:"triggered_by"`
	ExecutionStatus    string           `json:"execution_status" db:"execution_status"`
	CompletionStatus   string           `json:"completion_status" db:"completion_status"`
	Sequence           int64            `json:"sequence" db:"sequence"`
	Data               json.RawMessage  `json:"data" db:"data"`
	RunnerID           *string          `json:"runner_id" db:"runner_id"`
	RunnerName         *string          `json:"runner_name,omitempty" db:"runner_name"`
	Result             *json.RawMessage `json:"result" db:"result"`
	Duration           *int64           `json:"duration" db:"duration"`
	BillingDuration    *int64           `json:"billing_duration" db:"billing_duration"`
	TriggerType        *string          `json:"trigger_type,omitempty" db:"trigger_type"`
	AuthorEmail        *string          `json:"author_email,omitempty"`
	TriggererEmail     *string          `json:"triggerer_email,omitempty"`
	EntryNodeID        *string          `json:"entry_node_id,omitempty"`
	ParentExecutionID  *string          `json:"parent_execution_id,omitempty" db:"parent_execution_id"`
	ParentRelationship *string          `json:"parent_relationship,omitempty" db:"parent_relationship"`
	// Pointer rather than json.RawMessage by value: when no parent
	// linkage exists, a nil pointer becomes SQL NULL on insert.
	// A bare json.RawMessage zero-value would send an empty byte
	// slice, which the jsonb column rejects with "invalid input
	// syntax for type json".
	ParentMetadata  *json.RawMessage `json:"parent_metadata,omitempty" db:"parent_metadata"`
	RootExecutionID string           `json:"root_execution_id" db:"root_execution_id"`
	Depth           int              `json:"depth" db:"depth"`
	// HasChildren is populated only by list/tree queries that compute
	// it via EXISTS — point queries leave it false. The editor uses
	// it to drive the ▶/▼ expander glyph without a per-row child
	// count round-trip.
	HasChildren               bool             `json:"has_children" db:"has_children"`
	AgentID                   *string          `json:"agent_id,omitempty" db:"agent_id"`
	AgentSessionID            *string          `json:"agent_session_id,omitempty" db:"agent_session_id"`
	TriggeringUserID          *string          `json:"triggering_user_id,omitempty" db:"triggering_user_id"`
	TriggeringUserDisplayName *string          `json:"triggering_user_display_name,omitempty" db:"triggering_user_display_name"`
	CreditCostPence           *int64           `json:"credit_cost_pence,omitempty" db:"-"`
	Checkpoint                *json.RawMessage `json:"checkpoint,omitempty" db:"checkpoint"`
	ResumeAt                  *time.Time       `json:"resume_at,omitempty" db:"resume_at"`
	SuspendCount              int              `json:"suspend_count" db:"suspend_count"`
	Segments                  *json.RawMessage `json:"segments,omitempty" db:"segments"`
}

type Revision struct {
	ID        string      `json:"id" db:"id"`
	FloID     string      `json:"flo_id" db:"flo_id"`
	CreatedAt *time.Time  `json:"created_at" db:"created_at"`
	Data      interface{} `json:"data" db:"data"`
	IsLatest  bool        `json:"is_latest" db:"latest"`
}

type PendingExecution struct {
	Flow       Flo              `json:"flo"`
	Execution  Execution        `json:"execution"`
	Data       interface{}      `json:"data"`
	Checkpoint *json.RawMessage `json:"checkpoint,omitempty"`
}

type Node struct {
	ID          string      `json:"id" db:"id"`
	Type        string      `json:"type" db:"type"`
	Category    string      `json:"category" db:"category"`
	Label       string      `json:"label" db:"label"`
	Description string      `json:"description" db:"description"`
	Inputs      interface{} `json:"inputs" db:"inputs"`
	Outputs     interface{} `json:"outputs" db:"outputs"`
	Module      string      `json:"module" db:"module"`
}

type Port struct {
	ID       string      `json:"id" db:"id"`
	Type     string      `json:"type" db:"type"`
	Name     string      `json:"name" db:"name"`
	Label    string      `json:"label" db:"label"`
	Colour   string      `json:"colour" db:"colour"`
	Controls interface{} `json:"controls" db:"controls"`
}

type InputOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type InputVisibleWhen struct {
	Field  string   `json:"field"`
	Values []string `json:"values"`
}

// InputDynamicOptions tells the editor to fetch this input's dropdown
// choices from an API endpoint at edit time instead of relying solely on
// the static Options list (which remains the fallback when the fetch
// fails). The endpoint returns {"options": [{"name": ..., "value": ...}]}.
// Injected at serve time from dynamicOptionsMetadata — never stored in
// the actions table.
type InputDynamicOptions struct {
	Endpoint string `json:"endpoint"` // api-relative, e.g. /api/v1/action/options/openrouter-models
}

type InputDefinition struct {
	Name           string               `json:"name" db:"name"`
	Value          string               `json:"value" db:"value"`
	Type           string               `json:"type" db:"type"`
	Label          string               `json:"label"`
	Placeholder    string               `json:"placeholder"`
	Required       bool                 `json:"required,omitempty"`
	Options        []InputOption        `json:"options,omitempty"`
	VisibleWhen    *InputVisibleWhen    `json:"visible_when,omitempty"`
	DynamicOptions *InputDynamicOptions `json:"dynamic_options,omitempty"`
}

type OutputDefinition struct {
	Name  string `json:"name" db:"name"`
	Value string `json:"value" db:"value"`
	Type  string `json:"type" db:"type"`
}

type ActionCategory struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Icon           string `json:"icon"`
	Description    string `json:"description"`
	SubKey         string `json:"sub_key,omitempty"`
	SubName        string `json:"sub_name,omitempty"`
	SubIcon        string `json:"sub_icon,omitempty"`
	SubDescription string `json:"sub_description,omitempty"`
}

type Action struct {
	ID          string          `json:"id" db:"id"`
	Name        string          `json:"name" db:"name"`
	Label       string          `json:"label"`
	Type        int64           `json:"type"`
	ActionType  string          `json:"-" db:"action_type"`
	Description string          `json:"description" db:"description"`
	Icon        string          `json:"icon" db:"icon"`
	Plugin      string          `json:"plugin" db:"plugin"`
	Ordering    *int            `json:"ordering" db:"ordering"`
	Inputs      interface{}     `json:"inputs" db:"inputs"`
	Outputs     interface{}     `json:"outputs" db:"outputs"`
	Category    *ActionCategory `json:"category,omitempty"`
}

type Queue struct {
	ID               string    `json:"id" db:"id"`
	OrganisationID   *string   `json:"organisation_id" db:"organisation_id"`
	ParentID         *string   `json:"parent_id" db:"parent_id"`
	Name             string    `json:"name" db:"name"`
	RegistrationCode string    `json:"registration_code" db:"registration_code"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
	LocationCode     string    `json:"location_code" db:"location_code"`
}

type ActionDefinition struct {
	ID           string      `json:"id" db:"id"`
	Name         string      `json:"name" db:"name"`
	Hash         *string     `json:"hash" db:"hash"`
	Author       *string     `json:"author" db:"author"`
	Organisation *string     `json:"organisation" db:"organisation"`
	Description  *string     `json:"description" db:"description"`
	Website      *string     `json:"website" db:"website"`
	Icon         string      `json:"icon" db:"icon"`
	Date         *string     `json:"date" db:"date"`
	Type         int64       `json:"type" db:"action_type"`
	Ordering     *int64      `json:"order" db:"ordering"`
	Plugin       *string     `json:"-" db:"plugin"`
	Verified     bool        `json:"verified" db:"verified"`
	Inputs       interface{} `json:"inputs" db:"inputs"`
	Outputs      interface{} `json:"outputs" db:"outputs"`
}

type Runner struct {
	ID               string                      `json:"id" db:"id"`
	Identifier       string                      `json:"identifier" db:"identifier"`
	Name             string                      `json:"name" db:"name"`
	RegistrationCode string                      `json:"registration_code" db:"registration_code"`
	EnrolledAt       time.Time                   `json:"enrolled_at" db:"enrolled_at"`
	LastContactAt    *time.Time                  `json:"last_contact_at" db:"last_contact_at"`
	IPAddress        *string                     `json:"ip_address" db:"ip"`
	Status           string                      `json:"state" db:"state"`
	Active           bool                        `json:"active" db:"active"`
	Version          *string                     `json:"version" db:"version"`
	ExecutorVersion  *string                     `json:"executor_version" db:"executor_version"`
	Manifest         map[string]ActionDefinition `json:"manifest"`
	PublicKey        *string                     `json:"public_key" db:"public_key"`
	Verified         bool                        `json:"verified" db:"verified"`
}

type ExecutionResult struct {
	HasErrored bool        `json:"has_errored"`
	Cancelled  bool        `json:"cancelled,omitempty"`
	Suspended  bool        `json:"suspended,omitempty"`
	State      interface{} `json:"state"`
}

// ExecutionSegment records timing for one run/resume cycle of an execution.
type ExecutionSegment struct {
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	DurationMs int64  `json:"duration_ms"`
	Status     string `json:"status"` // "suspended", "success", "fail", "cancel"
	RunnerID   string `json:"runner_id,omitempty"`
}

type Environment struct {
	ID             string    `json:"id" db:"id"`
	Name           string    `json:"name" db:"name"`
	OwnerID        string    `json:"owner_id" db:"owner_id"`
	OrganisationID *string   `json:"organisation_id" db:"organisation_id"`
	SecretKey      string    `json:"-" db:"secret_key"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

type EnvironmentProperty struct {
	ID            string    `json:"id" db:"id"`
	EnvironmentID string    `json:"environment_id" db:"environment_id"`
	Name          string    `json:"name" db:"name"`
	Value         string    `json:"value" db:"value"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

type CreateEnvironmentSecret struct {
	EnvironmentID string     `json:"environment_id" db:"environment_id"`
	Name          string     `json:"name" db:"name"`
	Value         string     `json:"value" db:"value"`
	Provider      string     `json:"provider" db:"provider"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" db:"expires_at"`
}

type EnvironmentSecret struct {
	ID             string     `json:"id" db:"id"`
	EnvironmentID  string     `json:"environment_id" db:"environment_id"`
	Name           string     `json:"name" db:"name"`
	Value          string     `json:"-" db:"value"`
	DecryptedValue *string    `json:"value,omitempty"`
	Provider       string     `json:"provider" db:"provider"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
}

// CredentialProvider represents an OAuth provider configuration.
type CredentialProvider struct {
	Slug          string    `json:"slug" db:"slug"`
	Name          string    `json:"name" db:"name"`
	Icon          string    `json:"icon" db:"icon"`
	AuthURL       string    `json:"auth_url" db:"auth_url"`
	TokenURL      string    `json:"token_url" db:"token_url"`
	RevokeURL     *string   `json:"revoke_url,omitempty" db:"revoke_url"`
	DefaultScopes *string   `json:"default_scopes,omitempty" db:"default_scopes"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

// EnvironmentCredential represents an OAuth credential stored in an environment.
type EnvironmentCredential struct {
	ID              string           `json:"id" db:"id"`
	EnvironmentID   string           `json:"environment_id" db:"environment_id"`
	ProviderSlug    string           `json:"provider_slug" db:"provider_slug"`
	Name            string           `json:"name" db:"name"`
	ClientID        *string          `json:"-" db:"client_id"`
	ClientSecret    *string          `json:"-" db:"client_secret"`
	AccessToken     *string          `json:"-" db:"access_token"`
	RefreshToken    *string          `json:"-" db:"refresh_token"`
	TokenExpiresAt  *time.Time       `json:"token_expires_at,omitempty" db:"token_expires_at"`
	Scopes          *string          `json:"scopes,omitempty" db:"scopes"`
	Status          string           `json:"status" db:"status"`
	LastRefreshedAt *time.Time       `json:"last_refreshed_at,omitempty" db:"last_refreshed_at"`
	LastError       *string          `json:"last_error,omitempty" db:"last_error"`
	Metadata        *json.RawMessage `json:"metadata,omitempty" db:"metadata"`
	CreatedAt       time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at" db:"updated_at"`
	// ConsecutiveFailures counts recent unsuccessful token refresh
	// attempts. Reset to 0 on any successful refresh; incremented
	// by the refresh poller; used to escalate a credential to
	// revoked status after a threshold. Added by migration 94.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty" db:"consecutive_failures"`

	// Non-persisted: provider details joined for API responses
	ProviderName *string `json:"provider_name,omitempty" db:"provider_name"`
	ProviderIcon *string `json:"provider_icon,omitempty" db:"provider_icon"`
}

const (
	CredentialStatusPending = "pending"
	CredentialStatusActive  = "active"
	CredentialStatusExpired = "expired"
	CredentialStatusRevoked = "revoked"
	CredentialStatusError   = "error"
)

type Feedback struct {
	ID        string  `json:"id" db:"id"`
	UserID    *string `json:"user_id,omitempty" db:"user_id"`
	Name      string  `json:"name" db:"name"`
	Subject   string  `json:"subject" db:"subject"`
	Category  string  `json:"category" db:"category"`
	Message   string  `json:"message" db:"message"`
	URL       string  `json:"url,omitempty" db:"url"`
	UserAgent string  `json:"user_agent,omitempty" db:"user_agent"`
}

type UserDashboard struct {
	Usage     *int64 `json:"usage" db:"usage"`
	Allowance *int64 `json:"allowance" db:"allowance"`
}

// SubscriptionEntitlement is a cached entitlement pushed from the billing service.
type SubscriptionEntitlement struct {
	ID                 string           `json:"id" db:"id"`
	OwnerID            string           `json:"owner_id" db:"owner_id"`
	OrganisationID     *string          `json:"organisation_id,omitempty" db:"organisation_id"`
	PlanSlug           string           `json:"plan_slug" db:"plan_slug"`
	EntitlementKey     string           `json:"entitlement_key" db:"entitlement_key"`
	ValueInt           *int64           `json:"value_int,omitempty" db:"value_int"`
	ValueBool          *bool            `json:"value_bool,omitempty" db:"value_bool"`
	ValueJSON          *json.RawMessage `json:"value_json,omitempty" db:"value_json"`
	SubscriptionStatus string           `json:"subscription_status" db:"subscription_status"`
	PeriodEnd          *time.Time       `json:"period_end,omitempty" db:"period_end"`
	UpdatedAt          time.Time        `json:"updated_at" db:"updated_at"`
}

// CreditBalance is a local cache of the credit balance pushed from the billing service.
// The API only needs the balance for quota checks — rate calculation stays in billing.
type CreditBalance struct {
	ID             string    `json:"id" db:"id"`
	OwnerID        string    `json:"owner_id" db:"owner_id"`
	OrganisationID *string   `json:"organisation_id,omitempty" db:"organisation_id"`
	BalancePence   int64     `json:"balance_pence" db:"balance_pence"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

// CreditDeduction records an execution overage for async sync back to billing.
// The billing service calculates the actual cost using its dynamic rate schedule.
// AmountPence is populated after the billing service processes the deduction.
type CreditDeduction struct {
	ID             string    `json:"id" db:"id"`
	OwnerID        string    `json:"owner_id" db:"owner_id"`
	OrganisationID *string   `json:"organisation_id,omitempty" db:"organisation_id"`
	ExecutionID    string    `json:"execution_id" db:"execution_id"`
	ExecutionLabel *string   `json:"execution_label,omitempty" db:"execution_label"`
	DurationMs     int64     `json:"duration_ms" db:"duration_ms"`
	AmountPence    *int64    `json:"amount_pence,omitempty" db:"amount_pence"`
	Synced         bool      `json:"synced" db:"synced"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

type Group struct {
	ID             string    `json:"id" db:"id"`
	OrganisationID string    `json:"organisation_id" db:"organisation_id"`
	Name           string    `json:"name" db:"name"`
	Description    *string   `json:"description,omitempty" db:"description"`
	IsDefault      bool      `json:"is_default" db:"is_default"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	Permissions    []string  `json:"permissions,omitempty"`
	MemberCount    int       `json:"member_count,omitempty" db:"member_count"`
}

type GroupMember struct {
	UserID  string     `json:"user_id" db:"user_id"`
	Name    string     `json:"name" db:"name"`
	AddedAt *time.Time `json:"added_at,omitempty" db:"added_at"`
}

type AgentGroupMember struct {
	AgentID     string     `json:"agent_id" db:"agent_id"`
	Name        string     `json:"name" db:"name"`
	Description *string    `json:"description,omitempty" db:"description"`
	Status      string     `json:"status" db:"status"`
	AddedAt     *time.Time `json:"added_at,omitempty" db:"added_at"`
}

type OrganisationAgentMember struct {
	AgentID     string    `json:"agent_id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description *string   `json:"description,omitempty" db:"description"`
	Status      string    `json:"status" db:"status"`
	OwnerID     string    `json:"owner_id" db:"owner_id"`
	OwnerName   string    `json:"owner_name" db:"owner_name"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

type UserPermissions struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	IsAdmin     bool     `json:"is_admin"`
}

// Agent types

const (
	AgentStatusStopped = "stopped"
	AgentStatusRunning = "running"
	AgentStatusPaused  = "paused"
	AgentStatusError   = "error"

	AgentSessionActive  = "active"
	AgentSessionEnded   = "ended"
	AgentSessionCrashed = "crashed"

	AgentMessageInbound  = "inbound"
	AgentMessageOutbound = "outbound"
	AgentMessageSystem   = "system"
)

type Agent struct {
	ID                 string  `json:"id" db:"id"`
	Name               string  `json:"name" db:"name"`
	Description        *string `json:"description,omitempty" db:"description"`
	OwnerID            string  `json:"owner_id" db:"owner_id"`
	OrganisationID     *string `json:"organisation_id,omitempty" db:"organisation_id"`
	EnvironmentID      *string `json:"environment_id,omitempty" db:"environment_id"`
	QueueID            *string `json:"queue_id,omitempty" db:"queue_id"`
	SystemPrompt       *string `json:"system_prompt,omitempty" db:"system_prompt"`
	OrchestratorFlowID *string `json:"orchestrator_flow_id,omitempty" db:"orchestrator_flow_id"`
	// ExtractionFlowID is the flow Launch/the executor post-reply hook
	// dispatch after every turn to pull memories, pending actions, and
	// commitments out of the conversation. NULL until Phase 2d-γ seeds
	// the canonical extraction flow and backfills existing agents.
	ExtractionFlowID         *string `json:"extraction_flow_id,omitempty" db:"extraction_flow_id"`
	AIAPIKey                 *string `json:"ai_api_key,omitempty" db:"ai_api_key"`
	ConversationHistoryLimit int     `json:"conversation_history_limit" db:"conversation_history_limit"`
	// PriorConversationCount caps the number of session_summary
	// memories surfaced to the agent in the prior_conversations
	// trigger field. Each summary carries a conversation_id the
	// agent can pass to the get_conversation tool when it needs
	// the full message history behind a summary. 0 disables the
	// feature; range enforced at 0..50 by the editor.
	PriorConversationCount  int             `json:"prior_conversation_count" db:"prior_conversation_count"`
	MaxConcurrentExecutions int             `json:"max_concurrent_executions" db:"max_concurrent_executions"`
	IdleTimeoutSeconds      int             `json:"idle_timeout_seconds" db:"idle_timeout_seconds"`
	Channels                json.RawMessage `json:"channels" db:"channels"`
	AllowedFlowIDs          pq.StringArray  `json:"allowed_flow_ids,omitempty" db:"allowed_flow_ids"`
	RequiresApproval        bool            `json:"requires_approval" db:"requires_approval"`
	MaxExecutionsPerHour    int             `json:"max_executions_per_hour" db:"max_executions_per_hour"`
	Status                  string          `json:"status" db:"status"`
	StartedAt               *time.Time      `json:"started_at,omitempty" db:"started_at"`
	StoppedAt               *time.Time      `json:"stopped_at,omitempty" db:"stopped_at"`
	CreatedAt               time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at" db:"updated_at"`
	ArchivedAt              *time.Time      `json:"archived_at,omitempty" db:"archived_at"`
	MemoryRetentionDays     *int            `json:"memory_retention_days,omitempty" db:"memory_retention_days"`
	MaxPinnedMemories       *int            `json:"max_pinned_memories,omitempty" db:"max_pinned_memories"`
	// Computed fields (populated at read time)
	ActiveSessionID      *string    `json:"active_session_id,omitempty"`
	MessageCount         int64      `json:"message_count" db:"message_count"`
	ExecutionCount       int64      `json:"execution_count" db:"execution_count"`
	LastActiveAt         *time.Time `json:"last_active_at,omitempty"`
	OrchestratorFlowName *string    `json:"orchestrator_flow_name,omitempty" db:"orchestrator_flow_name"`
	EnvironmentName      *string    `json:"environment_name,omitempty" db:"environment_name"`
}

type AgentSession struct {
	ID           string      `json:"id" db:"id"`
	AgentID      string      `json:"agent_id" db:"agent_id"`
	StartedAt    time.Time   `json:"started_at" db:"started_at"`
	EndedAt      *time.Time  `json:"ended_at,omitempty" db:"ended_at"`
	Status       string      `json:"status" db:"status"`
	HeartbeatAt  time.Time   `json:"heartbeat_at" db:"heartbeat_at"`
	Summary      interface{} `json:"summary" db:"summary"`
	ErrorMessage *string     `json:"error_message,omitempty" db:"error_message"`
	// Computed
	MessageCount   int64 `json:"message_count" db:"message_count"`
	ExecutionCount int64 `json:"execution_count" db:"execution_count"`
}

type AgentState struct {
	AgentID    string      `json:"agent_id" db:"agent_id"`
	StateKey   string      `json:"state_key" db:"state_key"`
	StateValue interface{} `json:"state_value" db:"state_value"`
	UpdatedAt  time.Time   `json:"updated_at" db:"updated_at"`
}

type AgentMessage struct {
	ID             string      `json:"id" db:"id"`
	AgentID        string      `json:"agent_id" db:"agent_id"`
	SessionID      *string     `json:"session_id,omitempty" db:"session_id"`
	ConversationID *string     `json:"conversation_id,omitempty" db:"conversation_id"`
	Sequence       *int64      `json:"sequence,omitempty" db:"sequence"`
	Direction      string      `json:"direction" db:"direction"`
	ChannelType    string      `json:"channel_type" db:"channel_type"`
	Sender         *string     `json:"sender,omitempty" db:"sender"`
	Content        string      `json:"content" db:"content"`
	Metadata       interface{} `json:"metadata,omitempty" db:"metadata"`
	ExecutionID    *string     `json:"execution_id,omitempty" db:"execution_id"`
	// SourceConversationID points back at the conversation that
	// *initiated* this message when it was sent via an AI tool call to
	// a different recipient than the orchestrator's own conversation.
	// Populated only on cross-conversation relays (e.g. Andy on
	// Telegram → agent sends Slack DM to Bob); regular replies leave
	// this NULL.
	SourceConversationID *string   `json:"source_conversation_id,omitempty" db:"source_conversation_id"`
	CreatedAt            time.Time `json:"created_at" db:"created_at"`
}

// AgentUser is the canonical "person" an agent knows about, independent of
// which channel they reach the agent on. Memories, commitments, and linked
// identities hang off this record. See plans/agent_memory.md for the full
// design.
type AgentUser struct {
	ID             string    `json:"id" db:"id"`
	AgentID        string    `json:"agent_id" db:"agent_id"`
	OrganisationID *string   `json:"organisation_id,omitempty" db:"organisation_id"`
	DisplayName    *string   `json:"display_name,omitempty" db:"display_name"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

// AgentUserGoogleAccount represents a connected Google account for an
// agent_user. Each user can connect multiple accounts (work + personal).
// Tokens are encrypted at rest and scoped per-account.
type AgentUserGoogleAccount struct {
	ID             string     `json:"id" db:"id"`
	AgentUserID    string     `json:"agent_user_id" db:"agent_user_id"`
	GoogleEmail    string     `json:"google_email" db:"google_email"`
	RefreshToken   []byte     `json:"-" db:"refresh_token"` // never serialised to JSON
	AccessToken    []byte     `json:"-" db:"access_token"`  // cached, encrypted
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty" db:"token_expires_at"`
	Scopes         *string    `json:"scopes,omitempty" db:"scopes"`
	Label          *string    `json:"label,omitempty" db:"label"`
	Purpose        string     `json:"purpose" db:"purpose"` // "calendar", "email_read", "email_send"
	Status         string     `json:"status" db:"status"`
	LastError      *string    `json:"last_error,omitempty" db:"last_error"`
	ConnectedAt    time.Time  `json:"connected_at" db:"connected_at"`
}

// TriggerGoogleAccount represents a Google account connected directly to
// a trigger (not an agent_user). Used by email triggers in standalone flows.
type TriggerGoogleAccount struct {
	ID             string     `json:"id" db:"id"`
	TriggerID      string     `json:"trigger_id" db:"trigger_id"`
	GoogleEmail    string     `json:"google_email" db:"google_email"`
	RefreshToken   []byte     `json:"-" db:"refresh_token"`
	AccessToken    []byte     `json:"-" db:"access_token"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty" db:"token_expires_at"`
	Scopes         *string    `json:"scopes,omitempty" db:"scopes"`
	Label          *string    `json:"label,omitempty" db:"label"`
	Purpose        string     `json:"purpose" db:"purpose"`
	Status         string     `json:"status" db:"status"`
	LastError      *string    `json:"last_error,omitempty" db:"last_error"`
	ConnectedAt    time.Time  `json:"connected_at" db:"connected_at"`
}

// GoogleTokenResponse is returned by the internal token endpoint.
// The executor's calendar tool actions receive this and use the
// access_token to call Google APIs.
type GoogleTokenResponse struct {
	Email       string `json:"email"`
	Label       string `json:"label,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	Error       string `json:"error,omitempty"`
}

// MicrosoftAccount represents a connected Microsoft (Outlook/Teams)
// account for an agent_user. Mirrors AgentUserGoogleAccount in shape
// and storage model: tokens are stored as BYTEA with PGP_SYM_ENCRYPT
// at rest (migration 102) — decrypted via PGP_SYM_DECRYPT on SELECT,
// re-encrypted on INSERT/UPDATE. Application-side, the fields are
// presented as []byte so callers don't accidentally serialise raw
// token bytes to JSON.
type MicrosoftAccount struct {
	ID             string     `json:"id" db:"id"`
	AgentUserID    string     `json:"agent_user_id" db:"agent_user_id"`
	Email          string     `json:"email" db:"email"`
	Label          *string    `json:"label,omitempty" db:"label"`
	Purpose        string     `json:"purpose" db:"purpose"` // "mail_read", "mail_send", "calendar", ...
	AccessToken    []byte     `json:"-" db:"access_token"`  // PGP-encrypted at rest; never serialised to JSON
	RefreshToken   []byte     `json:"-" db:"refresh_token"` // PGP-encrypted at rest; never serialised to JSON
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty" db:"token_expires_at"`
	Status         string     `json:"status" db:"status"`
	LastError      *string    `json:"last_error,omitempty" db:"last_error"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
}

// UserIdentity is a declared mapping from a Flomation platform user to a
// channel handle, scoped per-organisation. Replaces the AI-initiated
// [LINK_OFFER] linking flow: users declare their identities directly
// from their profile, and the webhook ingestion path resolves an incoming
// sender by looking up (organisation_id, channel_type, external_id) here.
// On miss, an anonymous users row is upserted instead (see migration 83).
//
// OrganisationID is nullable (migration 84): NULL means "personal mode",
// for users running personal agents outside any organisation. The
// per-org partial uniqueness still applies for non-personal rows.
type UserIdentity struct {
	UserID string `json:"user_id"          db:"user_id"`
	// OrganisationID serialises as JSON null (not omitted) when nil so
	// clients can reliably distinguish "personal mode" from "field
	// missing because old API version" — the editor filters by
	// (org_id === null) for the personal-mode view and that strict
	// equality only works if the field is present as null.
	OrganisationID *string    `json:"organisation_id"  db:"organisation_id"`
	ChannelType    string     `json:"channel_type"     db:"channel_type"`
	ExternalID     string     `json:"external_id"      db:"external_id"`
	DisplayName    *string    `json:"display_name,omitempty" db:"display_name"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"  db:"verified_at"`
	CreatedAt      time.Time  `json:"created_at"       db:"created_at"`
}

// CreateUserIdentity is the input shape for declaring a new identity from
// the editor profile UI. OrganisationID is omitted (or null) for
// personal-mode declarations.
type CreateUserIdentity struct {
	UserID         string  `json:"user_id"          db:"user_id"`
	OrganisationID *string `json:"organisation_id,omitempty"  db:"organisation_id"`
	ChannelType    string  `json:"channel_type"     db:"channel_type"`
	ExternalID     string  `json:"external_id"      db:"external_id"`
	DisplayName    *string `json:"display_name,omitempty" db:"display_name"`
}

// AgentIdentity maps a per-channel external identifier (Slack user_id,
// Telegram sender_id, email address, etc.) to an AgentUser. A single
// AgentUser may accumulate multiple identities over time via the
// natural-language linking flow that lands in Phase 5; until then each
// identity corresponds to exactly one AgentUser and memories remain
// scoped per-identity.
type AgentIdentity struct {
	ID                string     `json:"id" db:"id"`
	AgentID           string     `json:"agent_id" db:"agent_id"`
	AgentUserID       string     `json:"agent_user_id" db:"agent_user_id"`
	ChannelType       string     `json:"channel_type" db:"channel_type"`
	ChannelExternalID string     `json:"channel_external_id" db:"channel_external_id"`
	ChannelScope      *string    `json:"channel_scope,omitempty" db:"channel_scope"`
	Verified          bool       `json:"verified" db:"verified"`
	LinkedAt          *time.Time `json:"linked_at,omitempty" db:"linked_at"`
	CreatedAt         time.Time  `json:"created_at" db:"created_at"`
}

// AgentConversation is a conversation thread in a specific channel, scoped
// by (agent_id, channel_type, channel_id, thread_id). This is distinct from
// AgentSession, which tracks runtime lifecycle (active/ended/crashed +
// heartbeat) rather than conversation scoping. Messages get both a
// SessionID (lifecycle) and a ConversationID (scoping).
type AgentConversation struct {
	ID            string          `json:"id" db:"id"`
	AgentID       string          `json:"agent_id" db:"agent_id"`
	AgentUserID   *string         `json:"agent_user_id,omitempty" db:"agent_user_id"`
	ChannelType   string          `json:"channel_type" db:"channel_type"`
	ChannelID     string          `json:"channel_id" db:"channel_id"`
	ThreadID      *string         `json:"thread_id,omitempty" db:"thread_id"`
	StartedAt     time.Time       `json:"started_at" db:"started_at"`
	LastMessageAt time.Time       `json:"last_message_at" db:"last_message_at"`
	EndedAt       *time.Time      `json:"ended_at,omitempty" db:"ended_at"`
	Metadata      json.RawMessage `json:"metadata" db:"metadata"`
}

// AgentMemory is a durable fact, preference, feedback, or summary that an
// agent has learnt about a specific user (agent_user_id set) or about
// itself (agent_user_id NULL, scope='global'). Written primarily by the
// Phase 2d extraction System Flow; readable by Launch's system prompt
// assembler and by the executor actions agent/remember, agent/recall,
// agent/forget.
//
// See plans/agent_memory.md §"Memory records" and migration 42.
type AgentMemory struct {
	ID                 string           `json:"id" db:"id"`
	AgentID            string           `json:"agent_id" db:"agent_id"`
	AgentUserID        *string          `json:"agent_user_id,omitempty" db:"agent_user_id"`
	Scope              string           `json:"scope" db:"scope"`
	MemoryType         string           `json:"memory_type" db:"memory_type"`
	Title              string           `json:"title" db:"title"`
	Body               string           `json:"body" db:"body"`
	SourceConversation *string          `json:"source_conversation,omitempty" db:"source_conversation"`
	SourceMessage      *string          `json:"source_message,omitempty" db:"source_message"`
	Confidence         float64          `json:"confidence" db:"confidence"`
	Pinned             bool             `json:"pinned" db:"pinned"`
	Embedding          *pgvector.Vector `json:"embedding,omitempty" db:"embedding"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
	LastUsedAt         *time.Time       `json:"last_used_at,omitempty" db:"last_used_at"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty" db:"expires_at"`
	ValidUntil         *time.Time       `json:"valid_until,omitempty" db:"valid_until"`
	Status             string           `json:"status" db:"status"`
	SupersededBy       *string          `json:"superseded_by,omitempty" db:"superseded_by"`
}

// AgentPendingAction is an intent detected by the extraction pipeline
// that needs user confirmation before the platform executes it. Used for
// identity linking, memory forgetting, and fact correction. The
// agent/remember, agent/forget executor actions write to this table in
// Phase 2c; Phase 5's natural-language identity linking uses it
// exclusively.
//
// See plans/agent_memory.md §"Pending actions" and migration 42.
type AgentPendingAction struct {
	ID                 string          `json:"id" db:"id"`
	AgentID            string          `json:"agent_id" db:"agent_id"`
	AgentUserID        string          `json:"agent_user_id" db:"agent_user_id"`
	Type               string          `json:"type" db:"type"`
	Payload            json.RawMessage `json:"payload" db:"payload"`
	Evidence           string          `json:"evidence" db:"evidence"`
	Status             string          `json:"status" db:"status"`
	SourceConversation *string         `json:"source_conversation,omitempty" db:"source_conversation"`
	SourceMessage      *string         `json:"source_message,omitempty" db:"source_message"`
	CreatedAt          time.Time       `json:"created_at" db:"created_at"`
	ResolvedAt         *time.Time      `json:"resolved_at,omitempty" db:"resolved_at"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty" db:"expires_at"`
	NotifiedAt         *time.Time      `json:"notified_at,omitempty" db:"notified_at"`
}

// AgentCommitment is a promise that the agent (made_by='assistant') or
// the user (made_by='user') has made and that needs honouring on a
// schedule or condition. Written by the Phase 2d extraction pipeline;
// the Phase 3 commitment poller selects due rows and dispatches synthetic
// triggers back into the agent's orchestrator flow.
//
// See plans/agent_memory.md §"Commitments" and migration 42.
type AgentCommitment struct {
	ID                 string           `json:"id" db:"id"`
	AgentID            string           `json:"agent_id" db:"agent_id"`
	AgentUserID        *string          `json:"agent_user_id,omitempty" db:"agent_user_id"`
	ConversationID     *string          `json:"conversation_id,omitempty" db:"conversation_id"`
	Kind               string           `json:"kind" db:"kind"`
	Description        string           `json:"description" db:"description"`
	Payload            json.RawMessage  `json:"payload" db:"payload"`
	TriggerType        string           `json:"trigger_type" db:"trigger_type"`
	DueAt              *time.Time       `json:"due_at,omitempty" db:"due_at"`
	Condition          *json.RawMessage `json:"condition,omitempty" db:"condition"`
	Status             string           `json:"status" db:"status"`
	SourceConversation *string          `json:"source_conversation,omitempty" db:"source_conversation"`
	SourceMessage      *string          `json:"source_message,omitempty" db:"source_message"`
	MadeBy             string           `json:"made_by" db:"made_by"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
	FiredAt            *time.Time       `json:"fired_at,omitempty" db:"fired_at"`
	FulfilledAt        *time.Time       `json:"fulfilled_at,omitempty" db:"fulfilled_at"`
	CancelledAt        *time.Time       `json:"cancelled_at,omitempty" db:"cancelled_at"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty" db:"expires_at"`
	Recurrence         *string          `json:"recurrence,omitempty" db:"recurrence"`
}

type AgentSchedule struct {
	ID             string     `json:"id" db:"id"`
	AgentID        string     `json:"agent_id" db:"agent_id"`
	AgentUserID    *string    `json:"agent_user_id,omitempty" db:"agent_user_id"`
	ConversationID *string    `json:"conversation_id,omitempty" db:"conversation_id"`
	Name           string     `json:"name" db:"name"`
	Description    string     `json:"description" db:"description"`
	ScheduleMode   string     `json:"schedule_mode" db:"schedule_mode"`
	IntervalVal    *string    `json:"interval_val,omitempty" db:"interval_val"`
	Unit           *string    `json:"unit,omitempty" db:"unit"`
	TimeOfDay      *string    `json:"time_of_day,omitempty" db:"time_of_day"`
	DaysOfWeek     *string    `json:"days_of_week,omitempty" db:"days_of_week"`
	Timezone       string     `json:"timezone" db:"timezone"`
	SourceChannel  *string    `json:"source_channel,omitempty" db:"source_channel"`
	Enabled        bool       `json:"enabled" db:"enabled"`
	LastFiredAt    *time.Time `json:"last_fired_at,omitempty" db:"last_fired_at"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
}

type AgentExecution struct {
	ID               string     `json:"id" db:"id"`
	AgentID          string     `json:"agent_id" db:"agent_id"`
	SessionID        *string    `json:"session_id,omitempty" db:"session_id"`
	MessageID        *string    `json:"message_id,omitempty" db:"message_id"`
	ExecutionID      string     `json:"execution_id" db:"execution_id"`
	FlowID           string     `json:"flow_id" db:"flow_id"`
	Status           string     `json:"status" db:"status"`
	RequiresApproval bool       `json:"requires_approval" db:"requires_approval"`
	ApprovedBy       *string    `json:"approved_by,omitempty" db:"approved_by"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty" db:"approved_at"`
	CreatedAt        time.Time  `json:"created_at" db:"created_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty" db:"completed_at"`
	// Computed
	FlowName        *string `json:"flow_name,omitempty"`
	ExecutionStatus *string `json:"execution_status,omitempty"`
}

type AgentChannel struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

type AgentChannelTelegram struct {
	BotToken       string   `json:"bot_token"`
	AllowedChatIDs []string `json:"allowed_chat_ids,omitempty"`
}

type AgentChannelEmail struct {
	IMAPHost     string   `json:"imap_host"`
	IMAPPort     int      `json:"imap_port"`
	Username     string   `json:"username"`
	Password     string   `json:"password"`
	TLS          bool     `json:"tls"`
	WatchFolders []string `json:"watch_folders,omitempty"`
	SMTPHost     string   `json:"smtp_host,omitempty"`
	SMTPPort     int      `json:"smtp_port,omitempty"`
	FromAddress  string   `json:"from_address,omitempty"`
}

// AgentAuditLog records a write operation on agent data for compliance
// and the user-facing audit trail. Written by Phase 6 endpoints and the
// retention poller.
type AgentAuditLog struct {
	ID           string          `json:"id" db:"id"`
	AgentID      string          `json:"agent_id" db:"agent_id"`
	AgentUserID  *string         `json:"agent_user_id,omitempty" db:"agent_user_id"`
	ActorType    string          `json:"actor_type" db:"actor_type"`
	ActorID      *string         `json:"actor_id,omitempty" db:"actor_id"`
	EventType    string          `json:"event_type" db:"event_type"`
	ResourceType string          `json:"resource_type" db:"resource_type"`
	ResourceID   *string         `json:"resource_id,omitempty" db:"resource_id"`
	Detail       json.RawMessage `json:"detail" db:"detail"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
}

// AgentDataExport is the JSON payload returned by the "Export my data"
// endpoint. Contains all data an agent holds about a specific user.
type AgentDataExport struct {
	User        *AgentUser         `json:"user"`
	Identities  []*AgentIdentity   `json:"identities"`
	Memories    []*AgentMemory     `json:"memories"`
	Commitments []*AgentCommitment `json:"commitments"`
	AuditLog    []*AgentAuditLog   `json:"audit_log"`
	ExportedAt  time.Time          `json:"exported_at"`
}

// Plan is the header row for an agent-authored, multi-step plan.
// Tasks are stored separately in plan_task and reference the plan
// via plan_id. See plans/agent_planning.md for the design.
//
// The Plan moves through draft → active → (blocked|completed|cancelled)
// driven by the Launch-side tick orchestrator and the API's task
// completion writeback hook.
type Plan struct {
	ID                   string     `db:"id" json:"id"`
	AgentID              string     `db:"agent_id" json:"agent_id"`
	OwnerUserID          *string    `db:"owner_user_id" json:"owner_user_id,omitempty"`
	OrganisationID       *string    `db:"organisation_id" json:"organisation_id,omitempty"`
	CreatedByExecutionID *string    `db:"created_by_execution_id" json:"created_by_execution_id,omitempty"`
	Title                string     `db:"title" json:"title"`
	Goal                 string     `db:"goal" json:"goal"`
	Status               string     `db:"status" json:"status"`
	NextCheckAt          *time.Time `db:"next_check_at" json:"next_check_at,omitempty"`
	SuspendCount         int        `db:"suspend_count" json:"suspend_count"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updated_at"`
	CompletedAt          *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CancelledAt          *time.Time `db:"cancelled_at" json:"cancelled_at,omitempty"`
	CancelledReason      *string    `db:"cancelled_reason" json:"cancelled_reason,omitempty"`
}

// PlanTask is a single dependency-graph node within a Plan. Each
// task pins a (flow_id, flow_revision_id) so a mid-plan edit to the
// underlying flow can't silently change the task's behaviour. The
// graph is encoded via depends_on (an array of sibling task UUIDs)
// — the tick endpoint walks this to find ready tasks.
type PlanTask struct {
	ID          string  `db:"id" json:"id"`
	PlanID      string  `db:"plan_id" json:"plan_id"`
	Name        string  `db:"name" json:"name"`
	Description *string `db:"description" json:"description,omitempty"`
	// TaskKind discriminates dispatch behaviour. See M1.5 plan doc.
	//   PlanTaskKindOrchestrator (default) — tick fires the agent's
	//     own orchestrator_flow via the Plan Task Trigger, carrying
	//     task context as trigger data. FlowID / FlowRevisionID are nil.
	//   PlanTaskKindFlow — tick fires the pinned (FlowID, FlowRevisionID)
	//     for deterministic dispatch. Both pointers non-nil.
	// The schema CHECK constraint enforces exactly-one shape at the
	// row level; the Go side mirrors with pointer nullability.
	TaskKind       string          `db:"task_kind" json:"task_kind"`
	FlowID         *string         `db:"flow_id" json:"flow_id,omitempty"`
	FlowRevisionID *string         `db:"flow_revision_id" json:"flow_revision_id,omitempty"`
	Status         string          `db:"status" json:"status"`
	DependsOn      pq.StringArray  `db:"depends_on" json:"depends_on"`
	NotBefore      *time.Time      `db:"not_before" json:"not_before,omitempty"`
	InputsJSON     json.RawMessage `db:"inputs_json" json:"inputs_json"`
	// OutputsJSON is *json.RawMessage (not json.RawMessage) because
	// the column is nullable — fresh tasks have no outputs until
	// the completion writeback runs. A non-pointer json.RawMessage
	// can't represent NULL, and sql.Scan fails with "unsupported
	// Scan, storing driver.Value type <nil> into type *json.RawMessage"
	// when reading a row that hasn't been written-back yet.
	OutputsJSON    *json.RawMessage `db:"outputs_json" json:"outputs_json,omitempty"`
	ExecutionID    *string         `db:"execution_id" json:"execution_id,omitempty"`
	AttemptCount   int             `db:"attempt_count" json:"attempt_count"`
	LastError      *string         `db:"last_error" json:"last_error,omitempty"`
	MaxAttempts    int             `db:"max_attempts" json:"max_attempts"`
	TimeoutSeconds *int            `db:"timeout_seconds" json:"timeout_seconds,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	StartedAt      *time.Time      `db:"started_at" json:"started_at,omitempty"`
	CompletedAt    *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
}

// PlanTaskKind constants identify how the tick endpoint dispatches a
// plan task. Used as both the SQL CHECK-constraint values and the
// discriminator the Go-side branches read.
const (
	PlanTaskKindOrchestrator = "orchestrator"
	PlanTaskKindFlow         = "flow"
)

// PlanEvent is the append-only audit trail for a Plan's lifecycle.
// Both plan-level events ("draft → active", "completed", "cancelled")
// and task-level events ("dispatched", "succeeded", "failed", "retry")
// land here so the plan-detail UI can show a unified timeline.
type PlanEvent struct {
	ID         int64           `db:"id" json:"id"`
	PlanID     string          `db:"plan_id" json:"plan_id"`
	PlanTaskID *string         `db:"plan_task_id" json:"plan_task_id,omitempty"`
	EventType  string          `db:"event_type" json:"event_type"`
	Data       json.RawMessage `db:"data" json:"data"`
	CreatedAt  time.Time       `db:"created_at" json:"created_at"`
}

// BlobMetadata is the HEAD-only projection of a blob_object row.
// Handle and sha256 are hex-encoded on the wire so the type round-
// trips cleanly through JSON. Returned by /api/v1/internal/blob/:handle
// HEAD requests and embedded in the POST upload response.
type BlobMetadata struct {
	HandleHex string    `json:"handle" db:"handle_hex"`
	Mime      string    `json:"mime" db:"mime"`
	SizeBytes int64     `json:"size" db:"size_bytes"`
	SHA256Hex string    `json:"sha256" db:"sha256_hex"`
	Purpose   string    `json:"purpose" db:"purpose"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	ExpiresAt time.Time `json:"expires_at" db:"expires_at"`
}
