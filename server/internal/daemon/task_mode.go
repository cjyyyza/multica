package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

var errPlanModeUnavailable = errors.New("plan mode is unavailable for this task runtime")

func prepareClaimedTaskMode(task Task, provider string) (Task, string, error) {
	mode, err := taskpolicy.NormalizeMode(task.Mode)
	if err != nil {
		return Task{}, "", fmt.Errorf("%w: %w", errPlanModeUnavailable, err)
	}
	if mode == taskpolicy.ModeExecute {
		return task, agent.PermissionModeExecute, nil
	}
	if task.RequirementID == "" || task.ChatSessionID == "" || task.IssueID != "" {
		return Task{}, "", fmt.Errorf("%w: plan task requirement/chat binding is missing", errPlanModeUnavailable)
	}
	if task.PlanPermission != taskpolicy.PlanPermissionClaude || !agent.SupportsPlanMode(provider) {
		return Task{}, "", fmt.Errorf("%w: provider %q has no verified read-only plan permission", errPlanModeUnavailable, provider)
	}
	task.RemoteMCPConnections = nil
	task.RemoteMCPDaemonToken = ""
	task.PluginHookTools = nil
	task.ConnectedApps = nil
	if task.Agent != nil {
		agentData := *task.Agent
		agentData.McpConfig = []byte(`{"mcpServers":{}}`)
		agentData.CustomArgs = nil
		task.Agent = &agentData
	}
	return task, agent.PermissionModePlan, nil
}

func validatePlanExecutionRuntime(task Task, usesCustomProfileCommand bool, localAssignment *localDirectoryAssignment) error {
	if !taskpolicy.IsPlan(task.Mode) {
		return nil
	}
	if usesCustomProfileCommand {
		return fmt.Errorf("%w: custom runtime profiles are not verified", errPlanModeUnavailable)
	}
	if localAssignment != nil {
		return fmt.Errorf("%w: local directories and worktree finalization are not read-only", errPlanModeUnavailable)
	}
	return nil
}

func planModeEnvironment(task Task) map[string]string {
	if !taskpolicy.IsPlan(task.Mode) {
		return nil
	}
	return map[string]string{"MULTICA_TASK_MODE": taskpolicy.ModePlan, "MULTICA_REQUIREMENT_ID": strings.TrimSpace(task.RequirementID)}
}

func allowActiveTaskExternalTools(w http.ResponseWriter, task activeRepoCheckoutTask) bool {
	if taskpolicy.AllowsExternalTools(task.Mode) {
		return true
	}
	http.Error(w, `{"error":"external tools are not available in Plan Mode","code":"plan_mode_tool_denied"}`, http.StatusForbidden)
	return false
}
