package daemon

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func TestPrepareClaimedTaskModeExecuteIsUnchanged(t *testing.T) {
	t.Parallel()
	original := Task{
		Mode: "", RequirementID: "",
		RemoteMCPConnections: []remotemcp.Connection{{ContributionKey: "remote"}},
		PluginHookTools:      []PluginHookTool{{Name: "hook"}},
		ConnectedApps:        []ConnectedAppData{{ToolkitSlug: "notion"}},
		Agent: &AgentData{
			McpConfig:  []byte(`{"mcpServers":{"private":{}}}`),
			CustomArgs: []string{"--model", "sonnet"},
		},
	}
	got, permission, err := prepareClaimedTaskMode(original, "codex")
	if err != nil || permission != agent.PermissionModeExecute {
		t.Fatalf("execute prepare = permission %q err %v", permission, err)
	}
	if len(got.RemoteMCPConnections) != 1 || len(got.PluginHookTools) != 1 || len(got.ConnectedApps) != 1 ||
		len(got.Agent.CustomArgs) != 2 || string(got.Agent.McpConfig) != string(original.Agent.McpConfig) {
		t.Fatalf("execute task was modified: %+v", got)
	}
}

func TestPrepareClaimedTaskModePlanStripsEveryExternalTool(t *testing.T) {
	t.Parallel()
	task := Task{
		Mode: taskpolicy.ModePlan, RequirementID: "requirement-1", ChatSessionID: "chat-1",
		PlanPermission:       taskpolicy.PlanPermissionClaude,
		RemoteMCPConnections: []remotemcp.Connection{{ContributionKey: "remote"}},
		RemoteMCPDaemonToken: "mdt_secret",
		PluginHookTools:      []PluginHookTool{{Name: "hook"}},
		ConnectedApps:        []ConnectedAppData{{ToolkitSlug: "notion"}},
		Agent: &AgentData{
			McpConfig:  []byte(`{"mcpServers":{"private":{}}}`),
			CustomArgs: []string{"--dangerously-skip-permissions"},
		},
	}
	got, permission, err := prepareClaimedTaskMode(task, "claude")
	if err != nil || permission != agent.PermissionModePlan {
		t.Fatalf("plan prepare = permission %q err %v", permission, err)
	}
	if len(got.RemoteMCPConnections) != 0 || got.RemoteMCPDaemonToken != "" || len(got.PluginHookTools) != 0 || len(got.ConnectedApps) != 0 {
		t.Fatalf("external daemon tools survived: %+v", got)
	}
	if len(got.Agent.CustomArgs) != 0 || string(got.Agent.McpConfig) != `{"mcpServers":{}}` {
		t.Fatalf("agent config survived: %+v", got.Agent)
	}
	if task.Agent.CustomArgs[0] != "--dangerously-skip-permissions" {
		t.Fatal("input agent pointer was mutated")
	}
}

// TestPlanModeMcpConfigIsIsolated pins the reason prepareClaimedTaskMode writes
// an empty managed config: the effective config must not be rebuilt from the
// host runtime (~/.claude.json plus enabled plugin .mcp.json files) or from a
// Remote MCP broker overlay, or the plan task gets back every external server
// the mode stripped. The overlay is what makes the assertion deterministic —
// the runtime layer depends on whatever the host happens to configure.
func TestPlanModeMcpConfigIsIsolated(t *testing.T) {
	t.Parallel()

	planTask, permission, err := prepareClaimedTaskMode(Task{
		Mode: taskpolicy.ModePlan, RequirementID: "requirement-1", ChatSessionID: "chat-1",
		PlanPermission: taskpolicy.PlanPermissionClaude,
		Agent: &AgentData{
			McpConfig: []byte(`{"mcpServers":{"private":{}}}`),
		},
	}, "claude")
	if err != nil || permission != agent.PermissionModePlan {
		t.Fatalf("plan prepare = permission %q err %v", permission, err)
	}

	overlay := json.RawMessage(`{"mcpServers":{"broker":{"url":"http://127.0.0.1:1/mcp"}}}`)

	planEffective, err := resolveTaskEffectiveMcpConfig("claude", permission, planTask.Agent.McpConfig, overlay, slog.Default())
	if err != nil {
		t.Fatalf("plan effective MCP config: %v", err)
	}
	if string(planEffective) != `{"mcpServers":{}}` {
		t.Fatalf("plan task MCP config was rebuilt from host/runtime state: %s", planEffective)
	}

	// Execute mode must keep both merge layers, so the assertion above cannot
	// pass by the branch never running at all.
	executeEffective, err := resolveTaskEffectiveMcpConfig("claude", agent.PermissionModeExecute, planTask.Agent.McpConfig, overlay, slog.Default())
	if err != nil {
		t.Fatalf("execute effective MCP config: %v", err)
	}
	if !strings.Contains(string(executeEffective), `"broker"`) {
		t.Fatalf("execute task lost the Remote MCP overlay: %s", executeEffective)
	}
}

func TestPrepareClaimedTaskModeFailsClosed(t *testing.T) {
	t.Parallel()
	base := Task{Mode: taskpolicy.ModePlan, RequirementID: "req", ChatSessionID: "chat", PlanPermission: taskpolicy.PlanPermissionClaude}
	for _, tc := range []struct {
		name     string
		task     Task
		provider string
	}{
		{"missing requirement", Task{Mode: "plan", ChatSessionID: "chat", PlanPermission: taskpolicy.PlanPermissionClaude}, "claude"},
		{"issue bound", Task{Mode: "plan", RequirementID: "req", ChatSessionID: "chat", IssueID: "issue", PlanPermission: taskpolicy.PlanPermissionClaude}, "claude"},
		{"missing native permission", Task{Mode: "plan", RequirementID: "req", ChatSessionID: "chat"}, "claude"},
		{"unsupported provider", base, "codex"},
		{"invalid mode", Task{Mode: "full-access"}, "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := prepareClaimedTaskMode(tc.task, tc.provider); err == nil {
				t.Fatal("unsafe plan task was accepted")
			}
		})
	}
}

func TestPlanRuntimeAndLocalWriteGates(t *testing.T) {
	t.Parallel()
	plan := Task{Mode: taskpolicy.ModePlan}
	if err := validatePlanExecutionRuntime(plan, true, nil); err == nil {
		t.Fatal("custom profile was accepted")
	}
	if err := validatePlanExecutionRuntime(plan, false, &localDirectoryAssignment{}); err == nil {
		t.Fatal("local directory was accepted")
	}
	if err := validatePlanExecutionRuntime(plan, false, nil); err != nil {
		t.Fatalf("isolated built-in runtime rejected: %v", err)
	}
	if err := validatePlanExecutionRuntime(Task{Mode: taskpolicy.ModeExecute}, true, &localDirectoryAssignment{}); err != nil {
		t.Fatalf("execute behavior changed: %v", err)
	}

	w := httptest.NewRecorder()
	if allowActiveTaskExternalTools(w, activeRepoCheckoutTask{Mode: taskpolicy.ModePlan}) {
		t.Fatal("plan local write allowed")
	}
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), taskpolicy.PlanModeToolDenied) {
		t.Fatalf("local write response = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	if !allowActiveTaskExternalTools(w, activeRepoCheckoutTask{Mode: taskpolicy.ModeExecute}) {
		t.Fatal("execute local write rejected")
	}
}

func TestPlanDaemonCapabilityIsAdvertised(t *testing.T) {
	t.Parallel()
	found := false
	for _, capability := range daemonCommonCapabilities() {
		found = found || capability == protocol.DaemonCapabilityPlanModeV1
	}
	if !found {
		t.Fatal("daemon does not negotiate plan-mode-v1")
	}
}
