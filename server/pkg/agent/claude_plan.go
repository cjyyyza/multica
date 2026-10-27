package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Keep the caller's skill restrictions without modifying its settings file.
// Candidate submission belongs to the daemon, so planning needs no write tools.
func writeClaudePlanSettings(existingPath string) (string, error) {
	settings := map[string]any{}
	if existingPath != "" {
		data, err := os.ReadFile(existingPath)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", err
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	permissions, _ := settings["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
	}
	deny, _ := permissions["deny"].([]any)
	permissions["deny"] = append(deny, "Bash", "Write", "Edit", "NotebookEdit", "ExitPlanMode")
	permissions["defaultMode"] = PermissionModePlan
	settings["permissions"] = permissions
	settings["disableAllHooks"] = true
	data, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "multica-claude-plan-settings-*.json")
	if err != nil {
		return "", err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func claudePlanModeToolDenial(tool string) (string, bool) {
	switch tool {
	case "Read", "Glob", "Grep":
		return "", false
	default:
		if strings.HasPrefix(strings.ToLower(tool), "mcp__") {
			return "Plan Mode runs without MCP servers", true
		}
		return "Plan Mode denies this tool, including external network tools and permission-mode changes", true
	}
}

func prepareClaudePlanSettings(opts *ExecOptions) (func(), error) {
	path, err := writeClaudePlanSettings(opts.ClaudeSettingsPath)
	if err != nil {
		return nil, fmt.Errorf("prepare Claude plan permissions: %w", err)
	}
	opts.ClaudeSettingsPath = path
	return func() { _ = os.Remove(path) }, nil
}
