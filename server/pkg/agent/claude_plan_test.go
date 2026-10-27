package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestClaudePlanToolScope(t *testing.T) {
	cwd := t.TempDir()
	const requirementID = "018f5eea-1234-7000-8000-000000000001"
	get := "multica requirement get " + requirementID
	candidate := "multica requirement candidate " + requirementID + " --expected-revision 3 --snapshot-file ./candidate.json"
	for _, tc := range []struct {
		name, tool string
		input      map[string]any
		allow      bool
	}{
		{"candidate file", "Write", map[string]any{"file_path": "./candidate.json", "content": "{}"}, false},
		{"absolute candidate file", "Write", map[string]any{"file_path": filepath.Join(cwd, "candidate.json"), "content": "{}"}, false},
		{"other local file", "Write", map[string]any{"file_path": "source.go", "content": "probe"}, false},
		{"outside task", "Write", map[string]any{"file_path": filepath.Join(filepath.Dir(cwd), "candidate.json"), "content": "probe"}, false},
		{"parent escape", "Write", map[string]any{"file_path": "../candidate.json", "content": "probe"}, false},
		{"symlink parent traversal", "Write", map[string]any{"file_path": "linked/../candidate.json", "content": "probe"}, false},
		{"missing file path", "Write", map[string]any{}, false},
		{"requirement read", "Bash", map[string]any{"command": get + " --output json"}, false},
		{"candidate submission", "Bash", map[string]any{"command": candidate + " --idempotency-key plan:3"}, false},
		{"equals flags", "Bash", map[string]any{"command": candidate + " --output=json"}, false},
		{"other requirement", "Bash", map[string]any{"command": "multica requirement get another-requirement"}, false},
		{"arbitrary shell write", "Bash", map[string]any{"command": "echo probe > source.go"}, false},
		{"network through shell", "Bash", map[string]any{"command": "curl https://example.test"}, false},
		{"appended command", "Bash", map[string]any{"command": get + " && curl https://example.test"}, false},
		{"command substitution", "Bash", map[string]any{"command": get + " --output $(curl https://example.test)"}, false},
		{"newline", "Bash", map[string]any{"command": get + "\nmultica issue create"}, false},
		{"environment expansion", "Bash", map[string]any{"command": "$MULTICA_CLI requirement get " + requirementID}, false},
		{"external snapshot", "Bash", map[string]any{"command": candidate + " --allow-external-file"}, false},
		{"stdin shell channel", "Bash", map[string]any{"command": "multica requirement candidate " + requirementID + " --expected-revision 3 --snapshot-stdin"}, false},
		{"duplicate flag", "Bash", map[string]any{"command": candidate + " --snapshot-file ../secret.json"}, false},
		{"readonly tool", "Read", map[string]any{"file_path": "CLAUDE.md"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &claudeBackend{cfg: Config{Logger: slog.Default(), Env: map[string]string{"MULTICA_REQUIREMENT_ID": requirementID}}}
			var output bytes.Buffer
			b.handleControlRequest(claudeSDKMessage{
				RequestID: "plan-scope",
				Request:   mustMarshal(t, claudeControlRequestPayload{Subtype: "can_use_tool", ToolName: tc.tool, Input: mustMarshal(t, tc.input)}),
			}, &output, ExecOptions{PermissionMode: PermissionModePlan, Cwd: cwd})
			var response struct {
				Response struct {
					Response struct {
						Behavior string `json:"behavior"`
					} `json:"response"`
				} `json:"response"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if (response.Response.Response.Behavior == "allow") != tc.allow {
				t.Fatalf("permission response = %s, want allow=%v", output.String(), tc.allow)
			}
		})
	}
}

func TestClaudePlanExecuteEnforcesPermissions(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	const original = `{"permissions":{"allow":["Bash(*)"],"deny":["Skill(private)"]}}`
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &claudeBackend{cfg: Config{
		ExecutablePath: executable, Logger: slog.Default(),
		Env: map[string]string{"CLAUDE_FAKE_MODE": "plan_permissions", "MULTICA_REQUIREMENT_ID": "req-1"},
	}}
	session, err := backend.Execute(context.Background(), "Plan", ExecOptions{
		PermissionMode: PermissionModePlan, Cwd: t.TempDir(), Timeout: 5 * time.Second,
		ClaudeSettingsPath: settings, McpConfig: json.RawMessage(`{"mcpServers":{"external":{"url":"https://example.test"}}}`),
		CustomArgs: []string{"--tools", "all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "plan permissions verified" {
		t.Fatalf("plan permission stream = %+v", result)
	}
	unchanged, err := os.ReadFile(settings)
	if err != nil || string(unchanged) != original {
		t.Fatalf("caller settings changed: %s, %v", unchanged, err)
	}
}

func runFakeClaudePlanPermissions() {
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(2) }
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fail("missing initial prompt")
	}
	flag := func(name string) string {
		for i, arg := range os.Args {
			if arg == name && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return ""
	}
	data, err := os.ReadFile(flag("--settings"))
	if err != nil {
		fail("plan settings disappeared before child could read them")
	}
	var settings struct {
		DisableAllHooks bool `json:"disableAllHooks"`
		Permissions     struct {
			Ask, Deny   []string
			DefaultMode string
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		fail(err.Error())
	}
	if !settings.DisableAllHooks || settings.Permissions.DefaultMode != "plan" || !slices.Contains(settings.Permissions.Deny, "Bash") || !slices.Contains(settings.Permissions.Deny, "Write") || !slices.Contains(settings.Permissions.Deny, "Skill(private)") {
		fail("plan settings did not deny Bash/Write and retain restrictions")
	}
	if flag("--tools") != "Read,Glob,Grep" {
		fail("plan tool list was overridden")
	}
	mcp, err := os.ReadFile(flag("--mcp-config"))
	if err != nil || string(mcp) != `{"mcpServers":{}}` {
		fail("plan inherited MCP configuration")
	}
	for _, tc := range []struct{ tool, input, behavior string }{
		{"Write", `{"file_path":"source.go","content":"probe"}`, "deny"},
		{"Write", `{"file_path":"candidate.json","content":"{}"}`, "deny"},
		{"Bash", `{"command":"curl https://example.test"}`, "deny"},
		{"Bash", `{"command":"multica requirement get req-1 --output json"}`, "deny"},
		{"Bash", `{"command":"multica requirement candidate req-1 --expected-revision 3 --snapshot-file ./candidate.json"}`, "deny"},
		{"ExitPlanMode", `{}`, "deny"},
	} {
		request := map[string]any{"type": "control_request", "request_id": "plan", "request": map[string]any{"subtype": "can_use_tool", "tool_name": tc.tool, "input": json.RawMessage(tc.input)}}
		_ = json.NewEncoder(os.Stdout).Encode(request)
		if !scanner.Scan() {
			fail("missing control response")
		}
		var response struct {
			Response struct{ Response struct{ Behavior string } }
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			fail(err.Error())
		}
		if response.Response.Response.Behavior != tc.behavior {
			fail("unexpected permission response: " + scanner.Text())
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "success", "result": "plan permissions verified"})
}
