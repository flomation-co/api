package api

import (
	"encoding/json"
	"time"

	"github.com/lib/pq"
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
	ID             string         `json:"id" db:"id"`
	Name           string         `json:"name" db:"name"`
	EmailAddress   *string        `json:"email_address" db:"email_address"`
	MarketingOptIn bool           `json:"marketing_opt_in" db:"marketing_opt_in"`
	CreatedAt      time.Time      `json:"created_at" db:"created_at"`
	Organisations  []Organisation `json:"organisations"`
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
	ID                 string     `json:"id" db:"id"`
	Name               string     `json:"name" db:"name"`
	OrganisationID     *string    `json:"organisation_id,omitempty" db:"organisation_id"`
	AuthorID           *string    `json:"author_id,omitempty" db:"author_id"`
	CreatedAt          *time.Time `json:"created_at" db:"created_at"`
	LatestRevision     *Revision  `json:"revision,omitempty"`
	Scale              float32    `json:"scale" db:"scale"`
	XPosition          float32    `json:"x" db:"x"`
	YPosition          float32    `json:"y" db:"y"`
	Triggers           []*Trigger `json:"triggers"`
	ExecutionCount     int64      `json:"execution_count" db:"execution_count"`
	LastRun            *string    `json:"last_run" db:"last_run"`
	Duration           *int64     `json:"duration" db:"duration"`
	DurationAdditional *int64     `json:"duration_additional" db:"duration_additional"`
	LastExecution      *Execution `json:"last_execution" db:"last_execution"`
	EnvironmentID       *string    `json:"environment_id" db:"environment_id"`
	EnvironmentName     *string    `json:"environment_name" db:"environment_name"`
	QueueID             *string    `json:"queue_id" db:"queue_id"`
	HasValidationErrors  bool              `json:"has_validation_errors,omitempty"`
	RecentExecutions     []ExecutionStatus `json:"recent_executions,omitempty"`
	NotifyOnSuccess      bool              `json:"notify_on_success" db:"notify_on_success"`
	NotifyOnFailure      bool              `json:"notify_on_failure" db:"notify_on_failure"`
	NotificationEmails   *string           `json:"notification_emails,omitempty" db:"notification_emails"`
}

type ExecutionStatus struct {
	ID               string `json:"id" db:"id"`
	ExecutionStatus  string `json:"execution_status" db:"execution_status"`
	CompletionStatus string `json:"completion_status" db:"completion_status"`
}

type Execution struct {
	ID               string      `json:"id" db:"id"`
	FloID            string      `json:"flo_id" db:"flo_id"`
	Name             string      `json:"name" db:"name"`
	OwnerID          string      `json:"owner_id" db:"owner_id"`
	OrganisationID   *string     `json:"organisation_id" db:"organisation_id"`
	CreatedAt        time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt        *time.Time  `json:"updated_at" db:"updated_at"`
	CompletedAt      *time.Time  `json:"completed_at" db:"completed_at"`
	TriggeredBy      *string     `json:"triggered_by" db:"triggered_by"`
	ExecutionStatus  string      `json:"execution_status" db:"execution_status"`
	CompletionStatus string      `json:"completion_status" db:"completion_status"`
	Sequence         int64       `json:"sequence" db:"sequence"`
	Data             json.RawMessage  `json:"data" db:"data"`
	RunnerID         *string          `json:"runner_id" db:"runner_id"`
	Result           *json.RawMessage `json:"result" db:"result"`
	Duration         *int64      `json:"duration" db:"duration"`
	BillingDuration  *int64      `json:"billing_duration" db:"billing_duration"`
	TriggerType        *string     `json:"trigger_type,omitempty" db:"trigger_type"`
	AuthorEmail        *string     `json:"author_email,omitempty"`
	TriggererEmail     *string     `json:"triggerer_email,omitempty"`
	EntryNodeID        *string     `json:"entry_node_id,omitempty"`
	ParentExecutionID  *string     `json:"parent_execution_id,omitempty" db:"parent_execution_id"`
	AgentID            *string     `json:"agent_id,omitempty" db:"agent_id"`
	AgentSessionID     *string     `json:"agent_session_id,omitempty" db:"agent_session_id"`
}

type Revision struct {
	ID        string      `json:"id" db:"id"`
	FloID     string      `json:"flo_id" db:"flo_id"`
	CreatedAt *time.Time  `json:"created_at" db:"created_at"`
	Data      interface{} `json:"data" db:"data"`
	IsLatest  bool        `json:"is_latest" db:"latest"`
}

type PendingExecution struct {
	Flow      Flo         `json:"flo"`
	Execution Execution   `json:"execution"`
	Data      interface{} `json:"data"`
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

type InputDefinition struct {
	Name        string            `json:"name" db:"name"`
	Value       string            `json:"value" db:"value"`
	Type        string            `json:"type" db:"type"`
	Label       string            `json:"label"`
	Placeholder string            `json:"placeholder"`
	Required    bool              `json:"required,omitempty"`
	Options     []InputOption     `json:"options,omitempty"`
	VisibleWhen *InputVisibleWhen `json:"visible_when,omitempty"`
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
	State      interface{} `json:"state"`
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
	ID                      string           `json:"id" db:"id"`
	Name                    string           `json:"name" db:"name"`
	Description             *string          `json:"description,omitempty" db:"description"`
	OwnerID                 string           `json:"owner_id" db:"owner_id"`
	OrganisationID          *string          `json:"organisation_id,omitempty" db:"organisation_id"`
	EnvironmentID           *string          `json:"environment_id,omitempty" db:"environment_id"`
	QueueID                 *string          `json:"queue_id,omitempty" db:"queue_id"`
	SystemPrompt            *string          `json:"system_prompt,omitempty" db:"system_prompt"`
	OrchestratorFlowID      *string          `json:"orchestrator_flow_id,omitempty" db:"orchestrator_flow_id"`
	MaxConcurrentExecutions int              `json:"max_concurrent_executions" db:"max_concurrent_executions"`
	IdleTimeoutSeconds      int              `json:"idle_timeout_seconds" db:"idle_timeout_seconds"`
	Channels                json.RawMessage  `json:"channels" db:"channels"`
	AllowedFlowIDs          pq.StringArray   `json:"allowed_flow_ids,omitempty" db:"allowed_flow_ids"`
	RequiresApproval        bool             `json:"requires_approval" db:"requires_approval"`
	MaxExecutionsPerHour    int              `json:"max_executions_per_hour" db:"max_executions_per_hour"`
	Status                  string           `json:"status" db:"status"`
	StartedAt               *time.Time       `json:"started_at,omitempty" db:"started_at"`
	StoppedAt               *time.Time       `json:"stopped_at,omitempty" db:"stopped_at"`
	CreatedAt               time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt               time.Time        `json:"updated_at" db:"updated_at"`
	ArchivedAt              *time.Time       `json:"archived_at,omitempty" db:"archived_at"`
	// Computed fields (populated at read time)
	ActiveSessionID         *string          `json:"active_session_id,omitempty"`
	MessageCount            int64            `json:"message_count" db:"message_count"`
	ExecutionCount          int64            `json:"execution_count" db:"execution_count"`
	LastActiveAt            *time.Time       `json:"last_active_at,omitempty"`
	OrchestratorFlowName    *string          `json:"orchestrator_flow_name,omitempty" db:"orchestrator_flow_name"`
	EnvironmentName         *string          `json:"environment_name,omitempty" db:"environment_name"`
}

type AgentSession struct {
	ID           string     `json:"id" db:"id"`
	AgentID      string     `json:"agent_id" db:"agent_id"`
	StartedAt    time.Time  `json:"started_at" db:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty" db:"ended_at"`
	Status       string     `json:"status" db:"status"`
	HeartbeatAt  time.Time  `json:"heartbeat_at" db:"heartbeat_at"`
	Summary      interface{} `json:"summary" db:"summary"`
	ErrorMessage *string    `json:"error_message,omitempty" db:"error_message"`
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
	ID          string      `json:"id" db:"id"`
	AgentID     string      `json:"agent_id" db:"agent_id"`
	SessionID   *string     `json:"session_id,omitempty" db:"session_id"`
	Direction   string      `json:"direction" db:"direction"`
	ChannelType string      `json:"channel_type" db:"channel_type"`
	Sender      *string     `json:"sender,omitempty" db:"sender"`
	Content     string      `json:"content" db:"content"`
	Metadata    interface{} `json:"metadata,omitempty" db:"metadata"`
	ExecutionID *string     `json:"execution_id,omitempty" db:"execution_id"`
	CreatedAt   time.Time   `json:"created_at" db:"created_at"`
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
	BotToken      string   `json:"bot_token"`
	AllowedChatIDs []string `json:"allowed_chat_ids,omitempty"`
}

type AgentChannelEmail struct {
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	TLS          bool   `json:"tls"`
	WatchFolders []string `json:"watch_folders,omitempty"`
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	FromAddress  string `json:"from_address,omitempty"`
}
