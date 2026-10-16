package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestPMRoutesUseTaskIdentityAndKeepManualSourceWritesHumanOnly(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	dbfx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtime := dbfx.Runtime(t, "PM route fake runtime")
	pmAgent := dbfx.Agent(t, "PM route agent", runtime)
	triager := dbfx.Agent(t, "PM route triager", runtime)
	project := dbfx.Project(t, "PM route project")
	root := dbfx.Issue(t, "PM route root", testutil.Cols{"project_id": project, "assignee_type": "agent", "assignee_id": pmAgent})
	task := dbfx.Task(t, pmAgent, testutil.Cols{"status": "running", "runtime_id": runtime, "issue_id": root,
		"originator_user_id": testUserID, "accountable_user_id": testUserID})
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "task_token", testutil.Cols{"token_hash": auth.HashToken(token), "task_id": task,
		"agent_id": pmAgent, "workspace_id": testWorkspaceID, "user_id": testUserID, "expires_at": time.Now().Add(time.Hour)})
	scope := yixiezuo.PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", AssigneeEmails: []string{integrationTestEmail},
		ProjectID: project, TriageMention: "[@任务单分诊员](mention://agent/" + triager + ")"}
	source, err := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/12345")
	if err != nil {
		t.Fatal(err)
	}
	item := yixiezuo.PMIssue{Source: source, Title: "Source PM router bug " + project, Status: "新建", Priority: "高",
		Assignee: yixiezuo.PMPerson{Email: integrationTestEmail}, Project: yixiezuo.PMProject{ID: "10"}, IssueType: "BUG", ObservedAt: time.Now().UTC()}
	id := dbfx.Issue(t, "PM route unclassified", testutil.Cols{"project_id": project, "status": "backlog"})
	// Direct fixture inserts do not consume the API's workspace counter.
	dbfx.Exec(t, `UPDATE workspace SET issue_counter=GREATEST(issue_counter, (SELECT COALESCE(MAX(number),0) FROM issue WHERE workspace_id=$1)) WHERE id=$1`, testWorkspaceID)
	t.Cleanup(func() {
		// The API creates these rows, so they do not have fixture-owned cleanup.
		for _, query := range []string{
			`DELETE FROM yixiezuo_pm_notification WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`,
			`DELETE FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`,
			`DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id=$1) AND issue_id IN (SELECT id FROM issue WHERE project_id=$2) AND id <> '` + task + `'`,
			`DELETE FROM comment WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`,
			`DELETE FROM activity_log WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`,
			`DELETE FROM issue WHERE workspace_id=$1 AND project_id=$2 AND id <> '` + root + `'`,
		} {
			if _, err := testPool.Exec(context.Background(), query, testWorkspaceID, project); err != nil {
				t.Errorf("cleanup PM route fixture: %v", err)
			}
		}
	})
	request := func(path string, body any) *http.Request {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		// The middleware must overwrite these spoofed actor values.
		r.Header.Set("X-Actor-Source", "member")
		r.Header.Set("X-Agent-ID", triager)
		r.Header.Set("X-Workspace-ID", "00000000-0000-4000-8000-000000000099")
		return r
	}
	base := "/api/workspaces/" + testWorkspaceID + "/yixiezuo"
	var synced yixiezuo.PMResult
	testutil.Call(t, testServer.Config.Handler.ServeHTTP, request(base+"/pm/issues", yixiezuo.PMSourceRequest{Scope: scope, Issue: item})).Want(http.StatusOK).JSON(&synced)
	if synced.State != "created" {
		t.Fatalf("sync=%+v", synced)
	}
	var creatorType, creatorID string
	dbfx.QueryRow(t, `SELECT creator_type, creator_id::text FROM issue WHERE id=$1`, synced.IssueID).Scan(&creatorType, &creatorID)
	if creatorType != "agent" || creatorID != pmAgent {
		t.Fatalf("creator=%s:%s", creatorType, creatorID)
	}
	var notified yixiezuo.PMTriageResult
	testutil.Call(t, testServer.Config.Handler.ServeHTTP, request(base+"/pm/triage/"+id, map[string]any{"scope": scope})).Want(http.StatusOK).JSON(&notified)
	if notified.State != "notified" {
		t.Fatalf("triage=%+v", notified)
	}
	var authorType, authorID, sourceTask, originator string
	dbfx.QueryRow(t, `SELECT author_type, author_id::text, source_task_id::text FROM comment WHERE id=$1`, notified.CommentID).Scan(&authorType, &authorID, &sourceTask)
	dbfx.QueryRow(t, `SELECT originator_user_id::text FROM agent_task_queue WHERE agent_id=$1 AND trigger_comment_id=$2`, triager, notified.CommentID).Scan(&originator)
	if authorType != "agent" || authorID != pmAgent || sourceTask != task || originator != testUserID {
		t.Fatalf("trigger lineage=%s:%s task=%s originator=%s", authorType, authorID, sourceTask, originator)
	}
	for _, suffix := range []string{"/preview", "/imports", "/imports/" + synced.IssueID + "/publish", "/bridge/claim"} {
		testutil.Call(t, testServer.Config.Handler.ServeHTTP, request(base+suffix, map[string]any{})).Want(http.StatusForbidden)
	}
	foreign := "/api/workspaces/00000000-0000-4000-8000-000000000099/yixiezuo/pm/issues"
	testutil.Call(t, testServer.Config.Handler.ServeHTTP, request(foreign, yixiezuo.PMSourceRequest{Scope: scope, Issue: item})).Want(http.StatusForbidden)
}
