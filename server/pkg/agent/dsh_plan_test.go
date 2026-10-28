package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

const dshPlanFixtureEnv = "MULTICA_TEST_DSH_PLAN_FIXTURE"

func runFakeDshPlan(mode string) {
	encoder := json.NewEncoder(os.Stdout)
	if mode == "no-ready" {
		time.Sleep(10 * time.Second)
		return
	}
	ready := dshFrame{Version: 1, Type: "ready", Runtime: "dsh"}
	if mode != "old-bridge" {
		ready.Capabilities = &dshCapabilities{PlanMode: taskpolicy.PlanPermissionDsh, StructuredOutput: true}
	}
	_ = encoder.Encode(ready)
	var command json.RawMessage
	if json.NewDecoder(os.Stdin).Decode(&command) != nil {
		return
	}
	_ = os.WriteFile(os.Getenv("MULTICA_TEST_DSH_COMMAND"), command, 0o600)
	var input dshExecuteCommand
	_ = json.Unmarshal(command, &input)
	result := dshFrame{Version: 1, Type: "result", RequestID: input.RequestID, Status: "completed", Output: "ordinary text is not structured output", SessionID: "plan-session"}
	if mode == "structured" {
		result.StructuredOutput = json.RawMessage(`{"reply":"Please review the plan.","candidate":null}`)
	}
	_ = encoder.Encode(result)
}

func TestDshPlanPermissionSupport(t *testing.T) {
	t.Parallel()
	if !SupportsPlanMode("dsh") {
		t.Fatal("built-in DSH must support native Plan Mode")
	}
	if mode, ok := PlanPermissionMode("dsh"); !ok || mode != PermissionModePlan {
		t.Fatalf("DSH plan permission = %q, %v", mode, ok)
	}
}

func TestDshPlanProtocol(t *testing.T) {
	for _, mode := range []string{"structured", "plain-text", "old-bridge", "no-ready"} {
		t.Run(mode, func(t *testing.T) {
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			commandPath := filepath.Join(t.TempDir(), "command.json")
			backend, err := New("dsh", Config{
				ExecutablePath: bin, TaskID: "plan-task", Logger: slog.Default(),
				Env: map[string]string{dshPlanFixtureEnv: mode, "MULTICA_TEST_DSH_COMMAND": commandPath},
			})
			if err != nil {
				t.Fatal(err)
			}
			opts := ExecOptions{
				PermissionMode: PermissionModePlan, Cwd: t.TempDir(), Timeout: 5 * time.Second,
				OutputSchema:    json.RawMessage(`{"type":"object","required":["reply","candidate"]}`),
				ResumeSessionID: "previous-session",
				McpConfig:       json.RawMessage(`{"mcpServers":{"remote":{"type":"http","url":"https://example.test/mcp"}}}`),
			}
			if mode == "no-ready" {
				opts.HandshakeTimeout = 100 * time.Millisecond
			}
			session, err := backend.Execute(context.Background(), "prepare a plan", opts)
			if mode == "old-bridge" || mode == "no-ready" {
				wantError := "update the Multica DSH runtime bridge"
				if mode == "no-ready" {
					wantError = "handshake timed out"
				}
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("unsupported bridge = %v", err)
				}
				if _, statErr := os.Stat(commandPath); !os.IsNotExist(statErr) {
					t.Fatal("prompt was sent before Plan Mode negotiation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if mode == "plain-text" {
				if result.Status != "failed" || !strings.Contains(result.Error, "without structured output") {
					t.Fatalf("ordinary text satisfied a Plan schema: %+v", result)
				}
				return
			}
			if result.Status != "completed" || string(result.StructuredOutput) != `{"reply":"Please review the plan.","candidate":null}` {
				t.Fatalf("Plan result = %+v", result)
			}
			raw, err := os.ReadFile(commandPath)
			if err != nil {
				t.Fatal(err)
			}
			var command dshExecuteCommand
			if err := json.Unmarshal(raw, &command); err != nil {
				t.Fatal(err)
			}
			if command.Mode != "plan" || string(command.OutputSchema) != string(opts.OutputSchema) || command.ResumeSessionID != opts.ResumeSessionID || len(command.MCPServers) != 1 {
				t.Fatalf("native Plan command lost schema, resume or tools: %s", raw)
			}
		})
	}
}
