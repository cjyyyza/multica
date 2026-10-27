package agent

const (
	PermissionModeExecute = "bypassPermissions"
	PermissionModePlan    = "plan"
)

func SupportsPlanMode(provider string) bool {
	return provider == "claude"
}

func PlanPermissionMode(provider string) (string, bool) {
	if !SupportsPlanMode(provider) {
		return "", false
	}
	return PermissionModePlan, true
}
