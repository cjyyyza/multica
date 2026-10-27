//go:build agentintegration

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestClaudeRequirementExecuteRealSmoke(t *testing.T) {
	requireRealAgentSmoke(t)
	bin := os.Getenv("MULTICA_CLAUDE_PATH")
	evidence := os.Getenv("MULTICA_CLAUDE_SMOKE_EVIDENCE")
	inputPath := os.Getenv("MULTICA_CLAUDE_SMOKE_CANDIDATE")
	if bin == "" || evidence == "" || inputPath == "" {
		t.Fatal("explicit Claude executable, candidate input and evidence directory are required")
	}
	candidate, err := os.ReadFile(inputPath)
	if err != nil || !json.Valid(candidate) {
		t.Fatalf("candidate input: %v", err)
	}
	t.Run(PermissionModeExecute, func(t *testing.T) {
		const mode = PermissionModeExecute
		root := t.TempDir()
		settings := filepath.Join(root, "acceptance-settings.json")
		if err := os.WriteFile(settings, []byte(`{"disableAllHooks":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "candidate.json"), candidate, 0600); err != nil {
			t.Fatal(err)
		}
		backend, err := New("claude", Config{
			ExecutablePath: bin, BuiltinRuntime: true,
			Env: map[string]string{"MULTICA_REQUIREMENT_ID": "018f5eea-1234-7000-8000-000000000001"},
		})
		if err != nil {
			t.Fatal(err)
		}
		prompt := "Read candidate.json, then use Write to create acceptance-result.txt containing exactly SEVEN_ACCEPTANCE_OK. Work only inside the current temporary directory. Do not use Bash, network, other agents or change external systems."
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
		defer cancel()
		session, err := backend.Execute(ctx, prompt, ExecOptions{
			Cwd: root, PermissionMode: mode, Timeout: 3 * time.Minute, MaxTurns: 8,
			ClaudeSettingsPath: settings, McpConfig: json.RawMessage(`{"mcpServers":{}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var messages []Message
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for message := range session.Messages {
				messages = append(messages, message)
			}
		}()
		result := <-session.Result
		<-drained
		raw, _ := json.MarshalIndent(map[string]any{"mode": mode, "result": result, "messages": messages}, "", "  ")
		name, file := "claude-execute-real.json", "acceptance-result.txt"
		if err := os.WriteFile(filepath.Join(evidence, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if result.Status != "completed" {
			t.Fatalf("mode=%s status=%s error=%s output=%s", mode, result.Status, result.Error, result.Output)
		}
		written, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("mode=%s did not create %s: %v; output=%s", mode, file, err, result.Output)
		}
		if string(written) != "SEVEN_ACCEPTANCE_OK" {
			t.Fatalf("unexpected execute artifact %q", written)
		}
		if err := os.WriteFile(filepath.Join(evidence, file), written, 0600); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClaudePlanStructuredOutputRealSmoke(t *testing.T) {
	requireRealAgentSmoke(t)
	bin, evidence := os.Getenv("MULTICA_CLAUDE_PATH"), os.Getenv("MULTICA_CLAUDE_SMOKE_EVIDENCE")
	if bin == "" || evidence == "" {
		t.Fatal("explicit Claude executable and evidence directory are required")
	}
	backend, err := New("claude", Config{ExecutablePath: bin, BuiltinRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	session, err := backend.Execute(ctx, `This is a read-only planning acceptance test. Return the structured result with reply exactly "Plan ready", and candidate containing goal "Create one temporary acceptance file", nonGoals ["No business changes"], scope ["Temporary directory only"], constraints ["No external writes"], acceptanceCriteria ["File contains SEVEN_ACCEPTANCE_OK"], projectId null, and agentId "018f5eea-1234-7000-8000-000000000001". Do not create or edit any file, execute a shell command, access the network, or change permission mode. Do not use any tools except StructuredOutput.`, ExecOptions{
		Cwd: root, PermissionMode: PermissionModePlan, Timeout: 2 * time.Minute, MaxTurns: 6,
		OutputSchema: json.RawMessage(protocol.RequirementPlanOutputSchema),
		McpConfig:    json.RawMessage(`{"mcpServers":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for message := range session.Messages {
			messages = append(messages, message)
		}
	}()
	result := <-session.Result
	<-drained
	raw, _ := json.MarshalIndent(map[string]any{"result": result, "messages": messages}, "", "  ")
	if err := os.WriteFile(filepath.Join(evidence, "claude-plan-structured-real.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || !json.Valid(result.StructuredOutput) {
		t.Fatalf("structured Plan status=%s error=%s output=%s structured=%s", result.Status, result.Error, result.Output, result.StructuredOutput)
	}
	var output struct {
		Reply     string `json:"reply"`
		Candidate struct {
			Goal string `json:"goal"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(result.StructuredOutput, &output); err != nil || output.Reply != "Plan ready" || output.Candidate.Goal != "Create one temporary acceptance file" {
		t.Fatalf("unexpected structured Plan result: %s (%v)", result.StructuredOutput, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only Plan changed its workdir: %v (%v)", entries, err)
	}
}
