package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/notify"
)

type issueMentionTestSender func(context.Context, notify.Target, notify.Notification) (notify.Receipt, error)

func (send issueMentionTestSender) Send(ctx context.Context, target notify.Target, notification notify.Notification) (notify.Receipt, error) {
	return send(ctx, target, notification)
}

func TestNotifyIssueMentionsViaPOPO(t *testing.T) {
	configureIssuePOPONotifications(t)
	const (
		issueID = "11111111-1111-4111-8111-111111111111"
		aliceID = "22222222-2222-4222-8222-222222222222"
		bobID   = "33333333-3333-4333-8333-333333333333"
		agentID = "44444444-4444-4444-8444-444444444444"
	)
	content := "[@Alice](mention://member/" + aliceID + ") please answer. " +
		"cc [@Bob](mention://member/" + bobID + ") and [@Worker](mention://agent/" + agentID + "). " +
		"Again [@Alice](mention://member/" + aliceID + ")"
	srv := newIssuePOPONotificationServer(t, issueID, aliceID, bobID)
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "mat_test-token")

	type call struct {
		target       notify.Target
		notification notify.Notification
	}
	var calls []call
	sender := issueMentionTestSender(func(_ context.Context, target notify.Target, notification notify.Notification) (notify.Receipt, error) {
		calls = append(calls, call{target: target, notification: notification})
		return notify.Receipt{MessageID: "message-" + target.Receiver}, nil
	})
	err := notifyIssueMentionsViaPOPO(context.Background(), client, issueID, content, sender)
	if err != nil {
		t.Fatalf("notifyIssueMentionsViaPOPO: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("POPO send count = %d, want 2 (deduplicated members only): %+v", len(calls), calls)
	}
	if calls[0].target.Receiver != "alice@example.com" || calls[1].target.Receiver != "bob@example.com" {
		t.Fatalf("POPO receivers = %q, %q", calls[0].target.Receiver, calls[1].target.Receiver)
	}
	for _, call := range calls {
		if call.target.RobotName != "Comment work partner" || len(call.target.AtUIDs) != 0 {
			t.Errorf("private work partner target = %+v", call.target)
		}
		if !strings.Contains(call.notification.Message, content) {
			t.Errorf("POPO message does not preserve the comment: %q", call.notification.Message)
		}
		if !strings.Contains(call.notification.Message, "https://app.example/team-a/issues/MUL-42") {
			t.Errorf("POPO message lost the issue link: %q", call.notification.Message)
		}
	}
}

func TestNotifyIssueMentionsViaPOPOSkipsNonMemberAndRequirementComments(t *testing.T) {
	t.Run("no member mention does no reads or sends", func(t *testing.T) {
		err := notifyIssueMentionsViaPOPO(context.Background(), nil, "issue-1", "plain comment", nil)
		if err != nil {
			t.Fatalf("notifyIssueMentionsViaPOPO: %v", err)
		}
	})

	t.Run("requirement issue keeps its dedicated notification flow", func(t *testing.T) {
		const (
			issueID = "11111111-1111-4111-8111-111111111111"
			userID  = "22222222-2222-4222-8222-222222222222"
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/issues/"+issueID {
				t.Errorf("unexpected request: %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": issueID, "identifier": "MUL-42", "workspace_id": "ws-1", "requirement_id": "req-1",
			})
		}))
		defer srv.Close()
		client := cli.NewAPIClient(srv.URL, "ws-1", "mat_test-token")
		sender := &notifyTestSender{}
		err := notifyIssueMentionsViaPOPO(context.Background(), client, issueID,
			"[@Alice](mention://member/"+userID+")", sender)
		if err != nil || sender.calls != 0 {
			t.Fatalf("requirement notification: err=%v sends=%d", err, sender.calls)
		}
	})

	t.Run("unknown requirement binding does not enter the generic flow", func(t *testing.T) {
		const (
			issueID = "11111111-1111-4111-8111-111111111111"
			aliceID = "22222222-2222-4222-8222-222222222222"
		)
		baseHandler := newIssuePOPONotificationHandler(t, issueID, aliceID, "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/issues/"+issueID {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": issueID, "identifier": "MUL-42", "workspace_id": "ws-1",
				})
				return
			}
			baseHandler.ServeHTTP(w, r)
		}))
		defer srv.Close()
		client := cli.NewAPIClient(srv.URL, "ws-1", "mat_test-token")
		sender := &notifyTestSender{}
		err := notifyIssueMentionsViaPOPO(context.Background(), client, issueID,
			"[@Alice](mention://member/"+aliceID+")", sender)
		if err == nil || !strings.Contains(err.Error(), "requirement binding") {
			t.Fatalf("error = %v, want an unknown requirement binding warning", err)
		}
		if sender.calls != 0 {
			t.Fatalf("unknown requirement binding caused %d generic sends", sender.calls)
		}
	})
}

func TestNotifyIssueMentionsViaPOPODoesNotRetryOrBlockOtherRecipients(t *testing.T) {
	configureIssuePOPONotifications(t)
	const (
		issueID = "11111111-1111-4111-8111-111111111111"
		aliceID = "22222222-2222-4222-8222-222222222222"
		bobID   = "33333333-3333-4333-8333-333333333333"
	)
	srv := newIssuePOPONotificationServer(t, issueID, aliceID, bobID)
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "mat_test-token")
	content := "[@Alice](mention://member/" + aliceID + ") [@Bob](mention://member/" + bobID + ")"
	var receivers []string
	sender := issueMentionTestSender(func(_ context.Context, target notify.Target, _ notify.Notification) (notify.Receipt, error) {
		receivers = append(receivers, target.Receiver)
		if target.Receiver == "alice@example.com" {
			return notify.Receipt{}, &notify.SendError{Err: errors.New("send failed"), Unknown: true}
		}
		return notify.Receipt{MessageID: "message-bob"}, nil
	})
	err := notifyIssueMentionsViaPOPO(context.Background(), client, issueID, content, sender)
	if err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("error = %v, want the confirmed send failure", err)
	}
	if len(receivers) != 2 || receivers[0] != "alice@example.com" || receivers[1] != "bob@example.com" {
		t.Fatalf("send attempts = %+v, want one per recipient and no retry", receivers)
	}
}

func TestRunIssueCommentAddPOPOFailureDoesNotFailComment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the automatic popo-cli bridge runs on Windows")
	}
	configureIssuePOPONotifications(t)
	const (
		issueID = "11111111-1111-4111-8111-111111111111"
		aliceID = "22222222-2222-4222-8222-222222222222"
	)
	posts := 0
	baseHandler := newIssuePOPONotificationHandler(t, issueID, aliceID, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+issueID+"/comments" {
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-1"})
			return
		}
		baseHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	t.Setenv("MULTICA_AGENT_ID", "agent-1")
	t.Setenv("MULTICA_POPO_CLI", "true")
	t.Setenv("PATH", t.TempDir())

	cmd := newIssueCommentAddTestCmd()
	_ = cmd.Flags().Set("content", "[@Alice](mention://member/"+aliceID+") need input")
	_ = cmd.Flags().Set("output", "table")
	if err := runIssueCommentAdd(cmd, []string{issueID}); err != nil {
		t.Fatalf("comment must stay successful when POPO fails: %v", err)
	}
	if posts != 1 {
		t.Fatalf("comment POST count = %d, want 1", posts)
	}
}

func TestRunIssueCommentAddPOPODisabledSkipsNotification(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the automatic popo-cli bridge runs on Windows")
	}
	const (
		issueID = "11111111-1111-4111-8111-111111111111"
		aliceID = "22222222-2222-4222-8222-222222222222"
	)
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+issueID+"/comments" {
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-1"})
			return
		}
		t.Errorf("unexpected notification request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	t.Setenv("MULTICA_AGENT_ID", "agent-1")
	t.Setenv("MULTICA_POPO_CLI", "false")

	cmd := newIssueCommentAddTestCmd()
	_ = cmd.Flags().Set("content", "[@Alice](mention://member/"+aliceID+") need input")
	_ = cmd.Flags().Set("output", "table")
	if err := runIssueCommentAdd(cmd, []string{issueID}); err != nil {
		t.Fatalf("comment with POPO disabled: %v", err)
	}
	if posts != 1 {
		t.Fatalf("comment POST count = %d, want 1", posts)
	}
}

func TestNotifyIssueMentionsViaPOPORequiresDeploymentConfig(t *testing.T) {
	const (
		issueID = "11111111-1111-4111-8111-111111111111"
		aliceID = "22222222-2222-4222-8222-222222222222"
	)
	notifyCLISetup(t)
	srv := newIssuePOPONotificationServer(t, issueID, aliceID, "")
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "mat_test-token")
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.json")} {
		t.Setenv("MULTICA_NOTIFY_CONFIG", path)
		sender := &notifyTestSender{}
		err := notifyIssueMentionsViaPOPO(context.Background(), client, issueID,
			"[@Alice](mention://member/"+aliceID+")", sender)
		if err == nil || !strings.Contains(err.Error(), "MULTICA_NOTIFY_CONFIG") || sender.calls != 0 {
			t.Fatalf("invalid deployment config %q: err=%v sends=%d", path, err, sender.calls)
		}
	}
}

func configureIssuePOPONotifications(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.json")
	configuration := `{"version":1,"defaultTarget":"team","targets":{"team":{"robotName":"Comment work partner","receiver":"1234567","atUids":["other@example.com"]}}}`
	if err := os.WriteFile(path, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_NOTIFY_CONFIG", path)
}

func newIssuePOPONotificationServer(t *testing.T, issueID, aliceID, bobID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(newIssuePOPONotificationHandler(t, issueID, aliceID, bobID))
}

func newIssuePOPONotificationHandler(t *testing.T, issueID, aliceID, bobID string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/" + issueID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": issueID, "identifier": "MUL-42", "workspace_id": "ws-1", "requirement_binding_known": true,
			})
		case "/api/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"daemon_app_url": "https://app.example"})
		case "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "team-a"})
		case "/api/workspaces/ws-1/members":
			members := []map[string]any{{"user_id": aliceID, "name": "Alice", "email": "alice@example.com"}}
			if bobID != "" {
				members = append(members, map[string]any{"user_id": bobID, "name": "Bob", "email": "bob@example.com"})
			}
			_ = json.NewEncoder(w).Encode(members)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}
