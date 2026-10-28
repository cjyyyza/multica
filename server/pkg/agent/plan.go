package agent

import "github.com/multica-ai/multica/server/pkg/taskpolicy"

const (
	PermissionModeExecute = "bypassPermissions"
	PermissionModePlan    = "plan"
)

func SupportsPlanMode(provider string) bool {
	return taskpolicy.PlanPermission(provider) != ""
}

func PlanPermissionMode(provider string) (string, bool) {
	if !SupportsPlanMode(provider) {
		return "", false
	}
	return PermissionModePlan, true
}
