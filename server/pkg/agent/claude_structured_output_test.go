package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
)

func TestClaudeManagedStructuredOutputCannotBeOverridden(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"reply":{"type":"string"}},"required":["reply"]}`)
	args := buildClaudeArgs(ExecOptions{
		OutputSchema: schema,
		ExtraArgs:    []string{"--json-schema", `{"type":"array"}`},
		CustomArgs:   []string{"--json-schema={\"type\":\"string\"}"},
	}, slog.Default())
	index := slices.Index(args, "--json-schema")
	if index < 0 || index+1 >= len(args) || args[index+1] != string(schema) {
		t.Fatalf("managed schema not preserved: %v", args)
	}
	if slices.Contains(args[index+2:], "--json-schema") || slices.Contains(args, `{"type":"array"}`) {
		t.Fatalf("custom schema reached launch arguments: %v", args)
	}
}

func TestClaudeStructuredOutputPermissionRequiresManagedSchema(t *testing.T) {
	for _, schema := range []json.RawMessage{nil, json.RawMessage(`{"type":"object"}`)} {
		b := &claudeBackend{cfg: Config{Logger: slog.Default()}}
		var response bytes.Buffer
		b.handleControlRequest(claudeSDKMessage{
			RequestID: "structured-output",
			Request:   mustMarshal(t, claudeControlRequestPayload{Subtype: "can_use_tool", ToolName: "StructuredOutput", Input: json.RawMessage(`{"reply":"Ready"}`)}),
		}, &response, ExecOptions{PermissionMode: PermissionModePlan, OutputSchema: schema})
		var result struct {
			Response struct{ Response struct{ Behavior string } }
		}
		if err := json.Unmarshal(response.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if (result.Response.Response.Behavior == "allow") != (len(schema) > 0) {
			t.Fatalf("unmanaged structured tool permission: %s", response.String())
		}
	}
}

func TestClaudeStructuredResultIsSeparateFromProse(t *testing.T) {
	structured := json.RawMessage(`{"reply":"Which platform?","candidate":null}`)
	resultEvent, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "result": "Internal output envelope",
		"structured_output": structured,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := claudeUsageFixtureBackend(t, nil, resultEvent, false)
	session, err := backend.Execute(t.Context(), "Plan", ExecOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "Internal output envelope" || !bytes.Equal(result.StructuredOutput, structured) {
		t.Fatalf("structured result lost or mixed with prose: %+v", result)
	}
}
