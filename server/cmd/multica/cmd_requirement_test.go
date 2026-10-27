package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRequirementBlockCLIQuestionFileAndDeterministicIdempotency(t *testing.T) {
	t.Chdir(t.TempDir())
	const question = "需要哪个部署环境？\n请补充 Windows 与 Linux 的差异。"
	if err := os.WriteFile("question.md", []byte(question), 0o600); err != nil {
		t.Fatal(err)
	}
	var keys []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces/ws-1/requirements/req-1/block" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mat_task_scope" {
			t.Errorf("authorization = %q", got)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"question_comment": map[string]any{"id": "comment-1"},
			"wakeup":           map[string]any{"id": "wake-1", "generation": 1},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_task_scope")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	for range 2 {
		cmd := newRequirementCommand()
		cmd.SetArgs([]string{"block", "req-1", "--target-user", "user-1", "--expected-issue-revision", "8", "--question-file", "./question.md"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || !strings.HasPrefix(keys[0], "requirement-block:") {
		t.Fatalf("idempotency keys = %+v", keys)
	}
	if len(bodies) != 2 || bodies[0]["question"] != question || bodies[0]["targetUserId"] != "user-1" || bodies[0]["expectedIssueRevision"] != float64(8) {
		t.Fatalf("request bodies = %+v", bodies)
	}
	changed := newRequirementCommand()
	changed.SetArgs([]string{"block", "req-1", "--target-user", "user-1", "--expected-issue-revision", "9", "--question-file", "./question.md"})
	if err := changed.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[2] == keys[0] {
		t.Fatalf("changed revision did not get a new idempotency key: %+v", keys)
	}
	t.Setenv("MULTICA_TASK_ID", "task-2")
	changedTask := newRequirementCommand()
	changedTask.SetArgs([]string{"block", "req-1", "--target-user", "user-1", "--expected-issue-revision", "8", "--question-file", "./question.md"})
	if err := changedTask.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 4 || keys[3] == keys[0] {
		t.Fatalf("changed task did not get a new idempotency key: %+v", keys)
	}
}

func TestRequirementBlockCLIRequiresTaskID(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("question.md", []byte("question"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_task_scope")
	t.Setenv("MULTICA_TASK_ID", "")
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"block", "req-1", "--question-file", "./question.md", "--target-user", "user-1", "--expected-issue-revision", "1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "MULTICA_TASK_ID") {
		t.Fatalf("block without MULTICA_TASK_ID error = %v", err)
	}
}

func TestRequirementBlockCLIQuestionStdinAndCustomKey(t *testing.T) {
	var body map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"question_comment": map[string]any{"id": "comment-stdin"},
			"wakeup":           map[string]any{"id": "wake-stdin", "generation": 1},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-stdin")
	t.Setenv("MULTICA_TOKEN", "mat_task_scope")
	t.Setenv("MULTICA_TASK_ID", "task-stdin")
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { os.Stdin = original })
	const question = "first line\\literal\n第二行"
	if _, err := output.WriteString(question); err != nil {
		t.Fatal(err)
	}
	output.Close()
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"block", "req-stdin", "--question-stdin", "--target-user", "user-stdin", "--expected-issue-revision", "3", "--idempotency-key", "custom-key"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if key != "custom-key" || body["question"] != question {
		t.Fatalf("key/body = %q %+v", key, body)
	}
}

func TestRequirementBlockCLIRejectsInvalidInputAndMemberToken(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("question.md", []byte("question"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_TOKEN", "mat_task_scope")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:1")
	t.Run("multiple question sources", func(t *testing.T) {
		cmd := newRequirementCommand()
		cmd.SetArgs([]string{"block", "req-1", "--question-file", "./question.md", "--question-stdin", "--target-user", "user-1", "--expected-issue-revision", "1"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("expected mutually exclusive question sources to fail")
		}
	})
	t.Run("member token", func(t *testing.T) {
		t.Setenv("MULTICA_TOKEN", "mul_member")
		cmd := newRequirementCommand()
		cmd.SetArgs([]string{"block", "req-1", "--question-file", "./question.md", "--target-user", "user-1", "--expected-issue-revision", "1"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "execute task token") {
			t.Fatalf("member token error = %v", err)
		}
	})
}
