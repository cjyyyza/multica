package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/notify"
)

type notifyTestSender struct {
	calls        int
	target       notify.Target
	notification notify.Notification
}

func (s *notifyTestSender) Send(_ context.Context, target notify.Target, notification notify.Notification) (notify.Receipt, error) {
	s.calls++
	s.target, s.notification = target, notification
	return notify.Receipt{MessageID: "test-message", RobotName: target.RobotName, Receiver: target.Receiver, AtUIDs: target.AtUIDs}, nil
}

func notifyCLISetup(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("MULTICA_APP_URL", "")
	workdir := t.TempDir()
	t.Chdir(workdir)
	agentsFile := filepath.Join(workdir, "AGENTS.md")
	markdown := "# Work partner\n```multica-notify\n" + `{"version":1,"defaultTarget":"team","appUrl":"https://multica.example","targets":{"team":{"robotName":"DeepSeek Harness","receiver":"8493291","atUids":["yujian01@corp.netease.com"]}}}` + "\n```\n"
	if err := os.WriteFile(agentsFile, []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	return agentsFile, t.TempDir()
}

func TestNotifySendGenericQuestionFromUTF8FileAndDryRun(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	message := "请确认目标环境。\n保留 \\n 和 `literal`。"
	if err := os.WriteFile("question.txt", []byte("\ufeff"+message), 0600); err != nil {
		t.Fatal(err)
	}
	sender := &notifyTestSender{}
	args := []string{"send", "--agents-file", agentsFile, "--state-dir", stateDir, "--event", "deployment.question", "--title", "环境确认", "--message-file", "question.txt", "--url", "https://multica.example/team/issues/MUL-1", "--idempotency-key", "question-1"}
	for i := 0; i < 2; i++ {
		command := newNotifyCommand(sender)
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var receipt notify.Receipt
		if err := json.Unmarshal(output.Bytes(), &receipt); err != nil || receipt.MessageID != "test-message" {
			t.Fatalf("receipt: %s %v", output.String(), err)
		}
	}
	if sender.calls != 1 || sender.target.RobotName != "DeepSeek Harness" || sender.target.Receiver != "8493291" || len(sender.target.AtUIDs) != 1 || sender.notification.Event != "deployment.question" || sender.notification.Message != message {
		t.Fatalf("generic send: %+v", sender)
	}
	command := newNotifyCommand(sender)
	command.SetOut(io.Discard)
	command.SetArgs(append(args, "--dry-run"))
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatal("dry run sent a message")
	}
}

func TestNotifyWatchStrictCredentialAndTaskScopeGates(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_MODE", "MULTICA_MANAGED_BY"} {
		t.Setenv(name, "")
	}
	t.Setenv("MULTICA_SERVER_URL", "https://api.multica.example")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_TOKEN", "member-token")
	for _, missing := range []string{"MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_TOKEN"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv(missing, " ")
			command := newNotifyCommand(&notifyTestSender{})
			command.SetErr(io.Discard)
			command.SetArgs([]string{"watch", "--agents-file", agentsFile, "--state-dir", stateDir, "--requirement", "req", "--once"})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "requires explicit") {
				t.Fatalf("missing credential used a profile: %v", err)
			}
		})
	}
	t.Setenv("MULTICA_TOKEN", "mat_task-scoped")
	command := newNotifyCommand(&notifyTestSender{})
	command.SetErr(io.Discard)
	command.SetArgs([]string{"watch", "--agents-file", agentsFile, "--state-dir", stateDir, "--requirement", "req", "--once"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "member credentials") {
		t.Fatalf("task credential persisted into a watcher: %v", err)
	}
}

func TestNotifyWatchCLIReadsAndSendsWithoutWritesToMultica(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer member-token" || r.Header.Get("X-Workspace-ID") != "ws" {
			t.Errorf("invalid watcher request %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/workspaces/ws":
			io.WriteString(w, `{"id":"ws","slug":"team"}`)
		case "/api/workspaces/ws/requirements/req":
			io.WriteString(w, `{"id":"req","workspaceId":"ws","revision":4,"state":"blocked","sourceSnapshot":{"title":"修复"},"blockedQuestion":{"commentId":"q","content":"确认发布窗口？"}}`)
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_MODE", "MULTICA_MANAGED_BY"} {
		t.Setenv(name, "")
	}
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_TOKEN", "member-token")
	sender := &notifyTestSender{}
	for i := 0; i < 2; i++ {
		command := newNotifyCommand(sender)
		command.SetOut(io.Discard)
		command.SetArgs([]string{"watch", "--agents-file", agentsFile, "--state-dir", stateDir, "--requirement", "req", "--once"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if sender.calls != 1 || sender.notification.Event != "requirement.blocked" || !strings.Contains(sender.notification.Message, "确认发布窗口？") || sender.notification.URL != "https://multica.example/team/chat?requirement=req" {
		t.Fatalf("watch delivery: %+v", sender)
	}
}

func TestNotifyBackgroundUsesAbsoluteConfigAndEnvironmentCredentials(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	options := &notifyOptions{agentsFile: "AGENTS.md", stateDir: stateDir, popoCLI: "fake-popo"}
	if _, _, _, err := options.load(); err != nil {
		t.Fatal(err)
	}
	if options.agentsFile != agentsFile {
		t.Fatal("background config is not anchored to the selected file")
	}
	client := cli.NewAPIClient("https://api.multica.example", "ws", "private-token")
	args := notifyBackgroundArgs(options, notify.Source{Kind: "requirement", ID: "req"}, "https://multica.example", "10s", "ready.json")
	for _, arg := range args {
		if strings.Contains(arg, "private-token") || arg == "--background" {
			t.Fatalf("unsafe background argv: %q", arg)
		}
	}
	environ := notifyBackgroundEnv([]string{"PATH=fake-bin", "MULTICA_TOKEN=stale", "multica_workspace_id=stale", "MULTICA_TASK_ID=task", "MULTICA_AGENT_ID=agent"}, client)
	joined := strings.Join(environ, "\n")
	if strings.Contains(joined, "stale") || strings.Contains(joined, "TASK_ID=") || !strings.Contains(joined, "MULTICA_TOKEN=private-token") || !strings.Contains(joined, "MULTICA_WORKSPACE_ID=ws") || !strings.Contains(joined, "PATH=fake-bin") {
		t.Fatalf("background environment: %v", environ)
	}
}

func TestNotifyIssueUUIDAndIdentifierShareCheckpoint(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspaces/ws":
			io.WriteString(w, `{"id":"ws","slug":"team"}`)
		case "/api/issues/issue-uuid", "/api/issues/MUL-1":
			io.WriteString(w, `{"id":"issue-uuid","workspace_id":"ws","identifier":"MUL-1","title":"Review","status":"in_review","revision":3,"requirement_binding_known":true}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_MODE", "MULTICA_MANAGED_BY"} {
		t.Setenv(name, "")
	}
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_TOKEN", "member-token")
	sender := &notifyTestSender{}
	for _, id := range []string{"MUL-1", "issue-uuid"} {
		command := newNotifyCommand(sender)
		command.SetOut(io.Discard)
		command.SetArgs([]string{"watch", "--agents-file", agentsFile, "--state-dir", stateDir, "--issue", id, "--once"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if sender.calls != 1 {
		t.Fatalf("source aliases duplicated notifications: %d", sender.calls)
	}
}

func TestNotifyDeploymentFileOverridesRepositoryAndPinsBackgroundConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("AGENTS.md", []byte("# Repository instructions without notification settings"), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := `{"version":1,"defaultTarget":"deployed","appUrl":"https://multica.example","targets":{"deployed":{"robotName":"Server work partner","receiver":"1234567","atUids":["person@corp.netease.com"]}}}`
	if err := os.WriteFile("notify.json", []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs("notify.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_NOTIFY_CONFIG", "notify.json")
	options := &notifyOptions{stateDir: t.TempDir(), popoCLI: "fake-popo"}
	_, path, target, err := options.load()
	if err != nil || path != absolute || options.configFile != absolute || options.agentsFile != "" || target.Receiver != "1234567" {
		t.Fatalf("deployment routing: %+v %s %v", options, path, err)
	}
	args := notifyBackgroundArgs(options, notify.Source{Kind: "requirement", ID: "req"}, "https://multica.example", "10s", "ready.json")
	found := false
	for i, arg := range args {
		if arg == "--agents-file" {
			t.Fatal("deployment fell back to repository instructions")
		}
		if arg == "--config" && i+1 < len(args) && args[i+1] == absolute {
			found = true
		}
	}
	if !found {
		t.Fatal("background config was not pinned to the deployment file")
	}
	command := newNotifyCommand(&notifyTestSender{})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"config"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ConfigFile string `json:"configFile"`
		Format     string `json:"format"`
	}
	if json.Unmarshal(output.Bytes(), &result) != nil || result.ConfigFile != absolute || result.Format != "json" {
		t.Fatalf("configuration output: %s", output.String())
	}
}

func TestNotifyExplicitConfigSelectionNeverFallsBack(t *testing.T) {
	agentsFile, stateDir := notifyCLISetup(t)
	t.Setenv("MULTICA_NOTIFY_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	// An explicit work partner file wins over the deployment environment.
	options := &notifyOptions{agentsFile: agentsFile, stateDir: stateDir}
	if _, path, _, err := options.load(); err != nil || path != agentsFile {
		t.Fatalf("explicit work partner file: %s %v", path, err)
	}
	for _, args := range [][]string{
		{"config"},
		{"config", "--config", "missing.json"},
		{"config", "--config", "missing.json", "--agents-file", agentsFile},
		{"config", "--config", ""},
		{"config", "--agents-file", ""},
	} {
		command := newNotifyCommand(&notifyTestSender{})
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("invalid selection silently fell back to AGENTS.md: %v", args)
		}
	}
}

func TestNotifySendUsesDeploymentJSONWithoutAgentInstructions(t *testing.T) {
	t.Chdir(t.TempDir())
	configuration := `{"version":1,"defaultTarget":"deployed","targets":{"deployed":{"robotName":"Server work partner","receiver":"1234567"}}}`
	if err := os.WriteFile("notify.json", []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_NOTIFY_CONFIG", filepath.Join(t.TempDir(), "unused.json"))
	sender := &notifyTestSender{}
	command := newNotifyCommand(sender)
	command.SetOut(io.Discard)
	command.SetArgs([]string{"send", "--config", "notify.json", "--state-dir", t.TempDir(), "--event", "question", "--message", "服务器需要确认"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || sender.target.RobotName != "Server work partner" || sender.target.Receiver != "1234567" {
		t.Fatalf("server notification: %+v", sender)
	}
}
