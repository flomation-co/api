package rbac

// Permission represents a granular access control permission.
type Permission string

const (
	FlowCreate         Permission = "flow.create"
	FlowEdit           Permission = "flow.edit"
	FlowDelete         Permission = "flow.delete"
	FlowExecute        Permission = "flow.execute"
	RunnerManage       Permission = "runner.manage"
	RunnerView         Permission = "runner.view"
	OrganisationManage Permission = "organisation.manage"
	OrganisationView   Permission = "organisation.view"
	EnvironmentManage  Permission = "environment.manage"
	EnvironmentView    Permission = "environment.view"
	BillingManage      Permission = "billing.manage"
	BillingView        Permission = "billing.view"
	AgentView          Permission = "agent.view"
	AgentCreate        Permission = "agent.create"
	AgentEdit          Permission = "agent.edit"
	AgentDelete        Permission = "agent.delete"
	AgentStartStop     Permission = "agent.start_stop"
	EmbedManage        Permission = "embed.manage"
	EmbedView          Permission = "embed.view"
	GatewayManage      Permission = "gateway.manage"
	GatewayView        Permission = "gateway.view"
)

// ValidPermissions is the canonical list of all supported permissions.
var ValidPermissions = []Permission{
	FlowCreate,
	FlowEdit,
	FlowDelete,
	FlowExecute,
	RunnerManage,
	RunnerView,
	OrganisationManage,
	OrganisationView,
	EnvironmentManage,
	EnvironmentView,
	BillingManage,
	BillingView,
	AgentView,
	AgentCreate,
	AgentEdit,
	AgentDelete,
	AgentStartStop,
	EmbedManage,
	EmbedView,
	GatewayManage,
	GatewayView,
}

// DefaultMemberPermissions are granted when an org has no groups configured.
var DefaultMemberPermissions = []string{
	string(FlowCreate),
	string(FlowEdit),
	string(FlowExecute),
	string(RunnerView),
	string(OrganisationView),
	string(EnvironmentView),
	string(AgentView),
	string(EmbedView),
	string(GatewayView),
}

// AllPermissions returns all permission strings (used for admin users).
func AllPermissions() []string {
	result := make([]string, len(ValidPermissions))
	for i, p := range ValidPermissions {
		result[i] = string(p)
	}
	return result
}

// HasPermission checks whether a permission exists in the given set.
func HasPermission(userPerms []string, required Permission) bool {
	for _, p := range userPerms {
		if p == string(required) {
			return true
		}
	}
	return false
}

// HasAnyPermission checks whether any of the required permissions exist in the given set.
func HasAnyPermission(userPerms []string, required ...Permission) bool {
	for _, r := range required {
		if HasPermission(userPerms, r) {
			return true
		}
	}
	return false
}

// IsValidPermission checks whether a string is a valid permission.
func IsValidPermission(perm string) bool {
	for _, p := range ValidPermissions {
		if string(p) == perm {
			return true
		}
	}
	return false
}
