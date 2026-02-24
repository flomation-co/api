package api

import "time"

type Organisation struct {
	ID        string     `json:"id" db:"id"`
	Name      string     `json:"name" db:"name"`
	Icon      *string    `json:"icon,omitempty" db:"icon"`
	CreatedAt *time.Time `json:"created_at" db:"created_at"`
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
	ID             string    `json:"id" db:"id"`
	Name           string    `json:"name" db:"name"`
	OwnerID        *string   `json:"owner_id" db:"owner_id"`
	OrganisationID *string   `json:"organisation_id" db:"organisation_id"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	Type           string    `json:"type" db:"type"`
}

type TriggerInvocation struct {
	ID             string      `json:"id" db:"id"`
	TriggerID      string      `json:"trigger_id" db:"trigger_id"`
	OwnerID        *string     `json:"owner_id" db:"owner_id"`
	OrganisationID *string     `json:"organisation_id" db:"organisation_id"`
	CreatedAt      time.Time   `json:"created_at" db:"created_at"`
	Data           interface{} `json:"data" db:"data"`
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
	EnvironmentID      *string    `json:"environment_id" db:"environment_id"`
	EnvironmentName    *string    `json:"environment_name" db:"environment_name"`
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
	Data             interface{} `json:"data" db:"data"`
	RunnerID         *string     `json:"runner_id" db:"runner_id"`
	Result           interface{} `json:"result" db:"result"`
	Duration         *int64      `json:"duration" db:"duration"`
	BillingDuration  *int64      `json:"billing_duration" db:"billing_duration"`
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

type InputDefinition struct {
	Name        string `json:"name" db:"name"`
	Value       string `json:"value" db:"value"`
	Type        string `json:"type" db:"type"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
}

type OutputDefinition struct {
	Name  string `json:"name" db:"name"`
	Value string `json:"value" db:"value"`
	Type  string `json:"type" db:"type"`
}

type Action struct {
	ID          string      `json:"id" db:"id"`
	Name        string      `json:"name" db:"name"`
	Label       string      `json:"label"`
	Type        int64       `json:"type"`
	ActionType  string      `json:"-" db:"action_type"`
	Description string      `json:"description" db:"description"`
	Icon        string      `json:"icon" db:"icon"`
	Plugin      string      `json:"plugin" db:"plugin"`
	Ordering    *int        `json:"ordering" db:"ordering"`
	Inputs      interface{} `json:"inputs" db:"inputs"`
	Outputs     interface{} `json:"outputs" db:"outputs"`
}

type Queue struct {
	ID               string    `json:"id" db:"id"`
	OrganisationID   *string   `json:"organisation_id" db:"organisation_id"`
	Name             string    `json:"name" db:"name"`
	RegistrationCode string    `json:"registration_code" db:"registration_code"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
	LocationCode     string    `json:"location_code" db:"location_code"`
}

type Runner struct {
	ID               string     `json:"id" db:"id"`
	Identifier       string     `json:"identifier" db:"identifier"`
	Name             string     `json:"name" db:"name"`
	RegistrationCode string     `json:"registration_code" db:"registration_code"`
	EnrolledAt       time.Time  `json:"enrolled_at" db:"enrolled_at"`
	LastContactAt    *time.Time `json:"last_contact_at" db:"last_contact_at"`
	IPAddress        *string    `json:"ip_address" db:"ip"`
	Status           string     `json:"state" db:"state"`
	Active           bool       `json:"active" db:"active"`
	Version          *string    `json:"version" db:"version"`
}

type ExecutionResult struct {
	HasErrored bool        `json:"has_errored"`
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

type UserDashboard struct {
	Usage     *int64 `json:"usage" db:"usage"`
	Allowance *int64 `json:"allowance" db:"allowance"`
}
