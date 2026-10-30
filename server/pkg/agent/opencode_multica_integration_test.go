//go:build agentintegration

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This driver is invoked by the opt-in native-requirement E2E. It exercises
// the installed OpenCode through the production adapter, using a private cwd
// and standalone service. Credentials are explicitly scoped to this run.
func TestOpenCodeMulticaControllerRealSmoke(t *testing.T) {
	requireRealAgentSmoke(t)
	bin, model := os.Getenv("MULTICA_OPENCODE_PATH"), os.Getenv("MULTICA_OPENCODE_MODEL")
	root, promptPath, evidence := os.Getenv("MULTICA_OPENCODE_CWD"), os.Getenv("MULTICA_OPENCODE_PROMPT"), os.Getenv("MULTICA_OPENCODE_EVIDENCE")
	if bin == "" || model == "" || root == "" || promptPath == "" || evidence == "" {
		t.Fatal("explicit OpenCode executable, model, cwd, prompt and evidence file are required")
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := New("opencode", Config{ExecutablePath: bin, BuiltinRuntime: true, CLIVersion: os.Getenv("MULTICA_OPENCODE_VERSION"), Env: map[string]string{
		"MULTICA_SERVER_URL": os.Getenv("MULTICA_SERVER_URL"), "MULTICA_TOKEN": os.Getenv("MULTICA_TOKEN"), "MULTICA_WORKSPACE_ID": os.Getenv("MULTICA_WORKSPACE_ID"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	session, err := backend.Execute(ctx, string(prompt), ExecOptions{Cwd: root, Model: model, Timeout: 4 * time.Minute, CustomArgs: []string{"--standalone", "--auto", "--agent", "multica"}})
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
	raw, err := json.MarshalIndent(map[string]any{"model": model, "result": result, "messages": messages}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MULTICA_TOKEN"} {
		if token := os.Getenv(name); token != "" {
			raw = []byte(strings.ReplaceAll(string(raw), token, "[REDACTED]"))
		}
	}
	if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("OpenCode status=%s error=%s; see sanitized evidence", result.Status, result.Error)
	}
	t.Logf("OpenCode completed; evidence: %s", evidence)
}
