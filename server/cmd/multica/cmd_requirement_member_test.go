package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/testenv"
)

func TestRequirementStrictCredentialsNeverFallBackToProfile(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer explicit-token" || r.Header.Get("X-Workspace-ID") != "explicit-workspace" {
			t.Errorf("strict request used profile credentials")
		}
		io.WriteString(w, `{"requirements":[],"hasMore":false}`)
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	testenv.SetHome(t, t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{ServerURL: srv.URL, WorkspaceID: "profile-workspace", Token: "profile-token"}); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"MULTICA_SERVER_URL", "MULTICA_TOKEN", "MULTICA_WORKSPACE_ID"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv(missing, " ")
			cmd := newRequirementCommand()
			cmd.SetArgs([]string{"list", "--strict"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("missing %s must fail before profile lookup: %v", missing, err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("missing explicit credentials caused %d HTTP requests", calls)
	}
	t.Setenv("MULTICA_TOKEN", "explicit-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "explicit-workspace")
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"list", "--strict"})
	cmd.SetOut(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("complete explicit credentials caused %d HTTP requests", calls)
	}
}

func TestRequirementHumanCLIStillUsesProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer profile-token" || r.Header.Get("X-Workspace-ID") != "profile-workspace" {
			t.Error("human CLI did not use the selected profile")
		}
		io.WriteString(w, `{"requirements":[],"hasMore":false}`)
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	testenv.SetHome(t, t.TempDir())
	for _, name := range []string{"MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_TOKEN"} {
		t.Setenv(name, "")
	}
	if err := cli.SaveCLIConfig(cli.CLIConfig{ServerURL: srv.URL, WorkspaceID: "profile-workspace", Token: "profile-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"list"})
	cmd.SetOut(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRequirementCollectionCommandsRejectTableBeforeHTTP(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	for _, args := range [][]string{{"list"}, {"targets"}, {"messages", "req"}, {"result", "req"}} {
		cmd := newRequirementCommand()
		cmd.SetArgs(append(args, "--output", "table"))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "supports only --output json") {
			t.Fatalf("table output for %v = %v", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("unsupported collection format made %d HTTP requests", calls)
	}
}

func requirementMemberCLIEnv(t *testing.T, serverURL string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, name := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_TASK_MODE", "MULTICA_MANAGED_BY"} {
		t.Setenv(name, "")
	}
	t.Setenv("MULTICA_SERVER_URL", serverURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "member-token")
}

func TestRequirementMemberCLIRejectsTaskActorsAndInvalidWritesBeforeHTTP(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{}`) }))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	for _, args := range [][]string{
		{"create", "--idea", "Fix", "--external-ref", "ref"},
		{"create", "--agent", "a", "--squad", "s", "--idea", "Fix", "--external-ref", "ref"},
		{"create", "--agent", "a", "--idea", "Fix", "--external-ref", "ref", "--idempotency-key", " "},
		{"create", "--agent", "a", "--idea", "Fix", "--external-ref", "ref", "--output", "yaml"},
		{"reply", "req", "--content", "Answer", "--expected-issue-revision", "8"},
		{"reply", "req", "--content", "Answer", "--question-comment", "q", "--expected-issue-revision", "0"},
		{"clarify", "req", "--content", "Scope"},
		{"list", "--limit", "101"},
		{"messages", "req", "--before-id", "q"},
		{"wait", "req", "--until", "invented"},
	} {
		cmd := newRequirementCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("invalid command succeeded: %v", args)
		}
	}
	t.Setenv("MULTICA_TOKEN", "mat_task")
	t.Setenv("MULTICA_TASK_ID", "task")
	for _, args := range [][]string{{"create", "--agent", "a", "--idea", "Fix", "--external-ref", "ref"}, {"clarify", "r", "--content", "Scope", "--expected-revision", "2"}, {"reply", "r", "--content", "Answer", "--question-comment", "q", "--expected-issue-revision", "8"}, {"list"}, {"wait", "r"}} {
		cmd := newRequirementCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("task ran member command %v", args)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid/task commands made %d requests", calls)
	}
}

func TestRequirementMemberCLIUnicodeFileSquadAndPagination(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Method == http.MethodPost {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["squadId"] != "squad-1" || body["idea"] != "修复中文输入\n保留现有配置" || body["projectId"] != "project-1" || len(body["participants"].([]any)) != 1 {
				t.Errorf("body=%v", body)
			}
			io.WriteString(w, `{"id":"r","workspaceId":"ws-1","revision":1}`)
		} else {
			io.WriteString(w, `{"requirements":[],"hasMore":true,"nextOffset":4}`)
		}
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	if err := os.WriteFile("idea.md", []byte("修复中文输入\n保留现有配置"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("participants.json", []byte(`[{"userId":"u","roles":["approver"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"create", "--squad", "squad-1", "--external-ref", "run /42", "--project", "project-1", "--idea-file", "idea.md", "--participants-file", "participants.json"}, {"list", "--external-ref", "run /42", "--limit", "2", "--offset", "2"}} {
		cmd := newRequirementCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) != 2 || paths[1] != "/api/workspaces/ws-1/requirements?externalRef=run+%2F42&limit=2&offset=2" {
		t.Fatalf("pagination paths=%v", paths)
	}
}

func TestRequirementWaitFailsOnMalformedUnknownFailedAndTerminalStates(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	for _, fixture := range []string{`{}`, `null`, `{"id":"r","workspaceId":"ws-1","revision":1,"state":"future_state"}`, `{"id":"r","workspaceId":"ws-1","revision":1,"state":"cancelled"}`, `{"id":"r","workspaceId":"ws-1","revision":1,"state":"clarifying","run":{"status":"failed"}}`} {
		body = fixture
		cmd := newRequirementCommand()
		cmd.SetArgs([]string{"wait", "r", "--until", "done", "--timeout", "1s"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("wait false success for %s", body)
		}
	}
	body = `{"id":"r","workspaceId":"ws-1","revision":1,"state":"executing"}`
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"wait", "r", "--until", "done", "--timeout", "20ms", "--interval", "1h"})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("timeout error=%v", err)
	}
}

func TestRequirementCLIReadsTargetsMessagesAndResultWithoutWriting(t *testing.T) {
	var called []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("read command wrote %s", r.Method)
		}
		called = append(called, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/workspaces/ws-1/requirements/r":
			io.WriteString(w, `{"id":"r","workspaceId":"ws-1","revision":3,"chatSessionId":"chat","issueId":"issue"}`)
		case "/api/workspaces/ws-1":
			io.WriteString(w, `{"id":"ws-1","slug":"team"}`)
		case "/api/chat/sessions/chat/messages/page":
			io.WriteString(w, `{"messages":[],"has_more":false}`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	for _, args := range [][]string{{"targets"}, {"messages", "r", "--limit", "10", "--before-created-at", "2026-10-29T12:00:00Z", "--before-id", "old"}, {"result", "r"}} {
		cmd := newRequirementCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	joined := strings.Join(called, "\n")
	for _, path := range []string{"/api/workspaces/ws-1", "/api/agents", "/api/squads", "/api/runtimes", "/api/chat/sessions/chat/messages/page?before_created_at=2026-10-29T12%3A00%3A00Z&before_id=old&limit=10", "/api/issues/issue/task-runs", "/api/issues/issue/comments"} {
		if !strings.Contains(joined, path) {
			t.Fatalf("missing %s in %v", path, called)
		}
	}
}

func TestRequirementMemberCLICreateClarifyReplyAndRead(t *testing.T) {
	var paths, keys []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer member-token" || r.Header.Get("X-Workspace-ID") != "ws-1" {
			t.Errorf("missing member/workspace headers: %v", r.Header)
		}
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodPost {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, body)
		}
		if strings.HasSuffix(r.URL.Path, "/replies") {
			io.WriteString(w, `{"requirement":{"id":"req-1","workspaceId":"ws-1","revision":3,"state":"blocked"},"reply":{"id":"reply-1","issueId":"issue-1","questionCommentId":"question-1","content":"使用生产环境","authorId":"user-1","issueRevision":9}}`)
		} else {
			io.WriteString(w, `{"id":"req-1","workspaceId":"ws-1","revision":3,"state":"clarifying"}`)
		}
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	commands := [][]string{
		{"create", "--agent", "agent-1", "--external-ref", "fix-1", "--idea", "修复登录"},
		{"create", "--agent", "agent-1", "--external-ref", "fix-1", "--idea", "修复登录"},
		{"clarify", "req-1", "--expected-revision", "3", "--content", "保留现有行为"},
		{"reply", "req-1", "--question-comment", "question-1", "--expected-issue-revision", "8", "--content", "使用生产环境"},
		{"reply", "req-1", "--question-comment", "question-1", "--expected-issue-revision", "8", "--content", "使用生产环境"},
		{"get", "req-1"},
	}
	for _, args := range commands {
		cmd := newRequirementCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(keys) != 5 || keys[0] == "" || keys[0] != keys[1] || keys[3] == "" || keys[3] != keys[4] || keys[2] == keys[3] {
		t.Fatalf("unstable/empty mutation keys: %v", keys)
	}
	if paths[0] != "POST /api/workspaces/ws-1/requirement-requests" || paths[2] != "POST /api/workspaces/ws-1/requirements/req-1/messages" || paths[3] != "POST /api/workspaces/ws-1/requirements/req-1/replies" || paths[5] != "GET /api/workspaces/ws-1/requirements/req-1" {
		t.Fatalf("paths=%v", paths)
	}
	if bodies[0]["agentId"] != "agent-1" || bodies[0]["idea"] != "修复登录" || bodies[3]["questionCommentId"] != "question-1" || bodies[3]["expectedIssueRevision"] != float64(8) {
		t.Fatalf("bodies=%v", bodies)
	}
}

func TestRequirementWaitStopsAtHumanGateAndHonorsCancellation(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		state := "clarifying"
		if calls > 1 {
			state = "awaiting_approval"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "req-1", "workspaceId": "ws-1", "revision": 3, "state": state})
	}))
	defer srv.Close()
	requirementMemberCLIEnv(t, srv.URL)
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"wait", "req-1", "--interval", "1ms", "--timeout", "1s"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(output.String(), "awaiting_approval") {
		t.Fatalf("calls=%d output=%s", calls, output.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd = newRequirementCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"wait", "req-1", "--until", "done", "--interval", "1h", "--timeout", "2h"})
	start := time.Now()
	if err := cmd.Execute(); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	if time.Since(start) > time.Second || calls != 2 {
		t.Fatalf("cancel did not stop polling: calls=%d", calls)
	}
}
