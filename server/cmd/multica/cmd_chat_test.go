package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	chatTestAgentID   = "7b37ce6a-b06b-43ed-b4ae-6bb7eacc67a4"
	chatTestSessionID = "4f2a2a9e-1111-4a2b-8c3d-000000000001"
	chatTestMessageID = "4f2a2a9e-1111-4a2b-8c3d-000000000002"
	chatTestTaskID    = "4f2a2a9e-1111-4a2b-8c3d-000000000003"
)

// newChatCreateTestCmd mirrors chatCreateCmd's flags on a throwaway command so
// flag state does not leak between tests.
func newChatCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("agent", "", "")
	cmd.Flags().String("prompt", "", "")
	cmd.Flags().Bool("prompt-stdin", false, "")
	cmd.Flags().String("prompt-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

// prepareChatCreateTest isolates the command from any real daemon/task context
// and points it at the test server.
func prepareChatCreateTest(t *testing.T, serverURL string) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_DAEMON_PORT", "")
	setCLITestServerEnv(t, serverURL)
}

func writeChatTestAgentList(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if got := r.URL.Query().Get("workspace_id"); got != "ws-1" {
		t.Errorf("agent lookup workspace_id = %q, want ws-1", got)
	}
	_ = json.NewEncoder(w).Encode([]map[string]any{
		{"id": chatTestAgentID, "name": "Backend Bot"},
	})
}

func writeChatTestSession(w http.ResponseWriter, title string) {
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":         chatTestSessionID,
		"agent_id":   chatTestAgentID,
		"title":      title,
		"status":     "active",
		"created_at": "2026-10-21T10:00:00Z",
	})
}

func TestRunChatCreateSendsAgentPromptAndTitle(t *testing.T) {
	var sessionBody map[string]any
	var messageBody map[string]any
	var titleBody map[string]any
	var calls []string
	agentLookups := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Workspace-ID"); got != "ws-1" {
			t.Errorf("X-Workspace-ID = %q, want ws-1", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			agentLookups++
			writeChatTestAgentList(t, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			calls = append(calls, "create")
			if err := json.NewDecoder(r.Body).Decode(&sessionBody); err != nil {
				t.Fatalf("decode session body: %v", err)
			}
			// The create response carries the seed the server would show before
			// the caller's title is applied. The command must not print it.
			writeChatTestSession(w, "Investigate the failing")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			calls = append(calls, "send")
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id": chatTestMessageID,
				"task_id":    chatTestTaskID,
				"queued":     false,
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			calls = append(calls, "title")
			if err := json.NewDecoder(r.Body).Decode(&titleBody); err != nil {
				t.Fatalf("decode title body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    chatTestSessionID,
				"title": "Deploy triage",
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", "Backend")
	_ = cmd.Flags().Set("prompt", `check the deploy\nthanks`)
	_ = cmd.Flags().Set("title", "Deploy triage")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}

	if agentLookups != 1 {
		t.Fatalf("agent lookups = %d, want 1", agentLookups)
	}
	if got := sessionBody["agent_id"]; got != chatTestAgentID {
		t.Errorf("session agent_id = %v, want %s", got, chatTestAgentID)
	}
	if _, ok := sessionBody["title"]; ok {
		t.Errorf("create body must omit title when a prompt is sent, got %v", sessionBody["title"])
	}
	if got := messageBody["content"]; got != "check the deploy\nthanks" {
		t.Errorf("message content = %q, want escaped newline decoded", got)
	}
	if got := titleBody["title"]; got != "Deploy triage" {
		t.Errorf("patched title = %v, want Deploy triage", got)
	}
	if strings.Join(calls, ",") != "create,send,title" {
		t.Errorf("calls = %v, want create then send then title", calls)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	for key, want := range map[string]string{
		"session_id": chatTestSessionID,
		"agent_id":   chatTestAgentID,
		"title":      "Deploy triage",
		"status":     "active",
		"message_id": chatTestMessageID,
		"task_id":    chatTestTaskID,
	} {
		if got, _ := result[key].(string); got != want {
			t.Errorf("output %s = %q, want %q", key, got, want)
		}
	}
	if got, ok := result["queued"]; !ok || got != false {
		t.Errorf("output queued = %v (present=%v), want explicit false", got, ok)
	}
}

func TestRunChatCreateWithoutPromptCreatesEmptySession(t *testing.T) {
	messageRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			writeChatTestAgentList(t, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode session body: %v", err)
			}
			if _, ok := body["title"]; ok {
				t.Errorf("title should be omitted when --title is not set, got %v", body["title"])
			}
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			messageRequests++
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	if messageRequests != 0 {
		t.Fatalf("message requests = %d, want 0 without --prompt", messageRequests)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got, _ := result["session_id"].(string); got != chatTestSessionID {
		t.Errorf("session_id = %q, want %q", got, chatTestSessionID)
	}
	if _, ok := result["task_id"]; ok {
		t.Errorf("task_id must be absent without a prompt: %s", out)
	}
	if _, ok := result["queued"]; ok {
		t.Errorf("queued must be absent without a prompt: %s", out)
	}
}

func TestRunChatCreateEmptySessionStoresTitleAfterCreate(t *testing.T) {
	var sessionBody map[string]any
	var titleBody map[string]any
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			calls = append(calls, "create")
			if err := json.NewDecoder(r.Body).Decode(&sessionBody); err != nil {
				t.Fatalf("decode session body: %v", err)
			}
			writeChatTestSession(w, "")
		case r.Method == http.MethodPatch && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			calls = append(calls, "title")
			if err := json.NewDecoder(r.Body).Decode(&titleBody); err != nil {
				t.Fatalf("decode title body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    chatTestSessionID,
				"title": "Deploy triage",
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("title", "  Deploy triage  ")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	if _, ok := sessionBody["title"]; ok {
		t.Errorf("create body must omit title, got %v", sessionBody["title"])
	}
	if got := titleBody["title"]; got != "Deploy triage" {
		t.Errorf("patched title = %v, want trimmed title", got)
	}
	if strings.Join(calls, ",") != "create,title" {
		t.Errorf("calls = %v, want create then title", calls)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got, _ := result["title"].(string); got != "Deploy triage" {
		t.Errorf("title = %q, want Deploy triage", got)
	}
}

func TestRunChatCreateRejectsBlankAndLongTitle(t *testing.T) {
	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("title", "   ")
	if _, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) }); err == nil || !strings.Contains(err.Error(), "--title is empty") {
		t.Fatalf("blank title error = %v", err)
	}

	cmd = newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("title", strings.Repeat("名", chatCreateTitleMaxRunes+1))
	if _, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) }); err == nil || !strings.Contains(err.Error(), "--title must be at most") {
		t.Fatalf("long title error = %v", err)
	}
}

func TestRunChatCreateReportsSessionWhenTitlePatchFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id": chatTestMessageID,
				"task_id":    chatTestTaskID,
				"queued":     false,
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to update chat session"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt", "hello")
	_ = cmd.Flags().Set("title", "Deploy triage")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err == nil {
		t.Fatal("expected title update failure")
	}
	seen := cli.FormatError(err, false)
	for _, want := range []string{chatTestSessionID, "prompt was sent", "setting the title failed", "Open that session"} {
		if !strings.Contains(seen, want) {
			t.Errorf("formatted error %q missing %q", seen, want)
		}
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty on failure", out)
	}
}

func TestRunChatCreateTableOutputWithFullUUID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode session body: %v", err)
			}
			if got := body["agent_id"]; got != chatTestAgentID {
				t.Errorf("agent_id = %v, want %s", got, chatTestAgentID)
			}
			writeChatTestSession(w, "")
		default:
			t.Errorf("a full UUID must not trigger an agent lookup: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("output", "table")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	for _, want := range []string{"SESSION", "AGENT", chatTestSessionID, chatTestAgentID} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestRunChatCreateRequiresAgent(t *testing.T) {
	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("prompt", "hello")

	_, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "--agent is required") {
		t.Fatalf("error = %v, want --agent required", err)
	}
}

func TestRunChatCreateReportsSessionWhenPromptSendFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			writeChatTestAgentList(t, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"chat agent has no runtime"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)
	t.Setenv("LC_ALL", "en_US.UTF-8")

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt", "hello")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err == nil {
		t.Fatal("expected send failure")
	}
	seen := cli.FormatError(err, false)
	for _, want := range []string{chatTestSessionID, "sending the prompt failed", "no runtime", "Open that session"} {
		if !strings.Contains(seen, want) {
			t.Errorf("formatted error %q missing %q", seen, want)
		}
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty on failure", out)
	}
}

func TestRunChatCreateLocalizesPromptSendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			writeChatTestAgentList(t, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"chat agent has no runtime"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)
	t.Setenv("LC_ALL", "zh_CN.UTF-8")

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt", "hello")

	_, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err == nil {
		t.Fatal("expected send failure")
	}
	seen := cli.FormatError(err, false)
	for _, want := range []string{chatTestSessionID, "已创建", "发送提示词失败", "请求冲突：", "请打开该对话"} {
		if !strings.Contains(seen, want) {
			t.Errorf("formatted error %q missing %q", seen, want)
		}
	}
	if strings.Contains(seen, "sending the prompt failed") {
		t.Errorf("formatted error still has the English sentence: %q", seen)
	}
}

func TestRunChatCreateReadsPromptFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("第一行\n第二行\n"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	var messageBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			writeChatTestAgentList(t, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id": chatTestMessageID,
				"task_id":    chatTestTaskID,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			writeChatTestSession(w, "第一行")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_DAEMON_PORT", "")
	setCLITestServerEnv(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt-file", "prompt.md")

	if _, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) }); err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	if got := messageBody["content"]; got != "第一行\n第二行" {
		t.Errorf("message content = %q, want file body without trailing newline", got)
	}
}

func TestRunChatCreatePrintsTitleDerivedFromPrompt(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id": chatTestMessageID,
				"task_id":    chatTestTaskID,
				"queued":     false,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			gets++
			writeChatTestSession(w, "Investigate the failing")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt", "Investigate the failing deploy")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	if gets != 1 {
		t.Fatalf("title reads = %d, want 1 after a prompt with no --title", gets)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got, _ := result["title"].(string); got != "Investigate the failing" {
		t.Errorf("title = %q, want the title read back after send", got)
	}
}

func TestRunChatCreateSucceedsWhenTitleRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			writeChatTestSession(w, "")
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id": chatTestMessageID,
				"task_id":    chatTestTaskID,
				"queued":     true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to load chat session"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prepareChatCreateTest(t, srv.URL)

	cmd := newChatCreateTestCmd()
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_ = cmd.Flags().Set("prompt", "hello")

	out, err := captureStdout(t, func() error { return runChatCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runChatCreate: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if _, ok := result["title"]; ok {
		t.Errorf("title must be absent when the refresh fails: %s", out)
	}
	if got, _ := result["message_id"].(string); got != chatTestMessageID {
		t.Errorf("message_id = %q, want the queued send to still be reported", got)
	}
	if got, ok := result["queued"]; !ok || got != true {
		t.Errorf("queued = %v (present=%v), want true", got, ok)
	}
}
