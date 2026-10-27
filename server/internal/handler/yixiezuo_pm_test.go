package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func pmFixture(t *testing.T) (yixiezuo.PMScope, yixiezuo.PMIssue) {
	t.Helper()
	project := dbfx.Project(t, "PM destination")
	agent := createHandlerTestAgent(t, "PM triager "+project, nil)
	scope := yixiezuo.PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", AssigneeEmails: []string{handlerTestEmail}, IssueType: "BUG", ProjectID: project, TriageMention: "[@任务单分诊员](mention://agent/" + agent + ")"}
	source, err := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/12345")
	if err != nil {
		t.Fatal(err)
	}
	item := yixiezuo.PMIssue{Source: source, Title: "PM bug " + project, Description: "Source repro", Status: "新建", Priority: "高",
		Assignee: yixiezuo.PMPerson{ID: "376", Name: handlerTestName, Email: handlerTestEmail}, Project: yixiezuo.PMProject{ID: "10", Name: "DJ01单机-Bug"}, IssueType: "BUG-崩溃", ObservedAt: time.Now().UTC()}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_pm_notification WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`, testWorkspaceID, project)
		testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE workspace_id=$1 AND details->>'source'='yixiezuo_pm'`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE workspace_id=$1 AND issue_id IN (SELECT id FROM issue WHERE project_id=$2)`, testWorkspaceID, project)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE project_id=$1)`, project)
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_import WHERE workspace_id=$1 AND external_id=$2`, testWorkspaceID, item.Source.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id=$1 AND project_id=$2`, testWorkspaceID, project)
	})
	return scope, item
}

func syncPM(t *testing.T, scope yixiezuo.PMScope, item yixiezuo.PMIssue) yixiezuo.PMResult {
	t.Helper()
	var out yixiezuo.PMResult
	r := withURLParam(newRequest("POST", "/pm/issues", yixiezuo.PMSourceRequest{Scope: scope, Issue: item}), "id", testWorkspaceID)
	testutil.Call(t, testHandler.SyncYixiezuoPMIssue, r).Want(200).JSON(&out)
	return out
}

func notifyPM(t *testing.T, scope yixiezuo.PMScope, id string) yixiezuo.PMTriageResult {
	t.Helper()
	var out yixiezuo.PMTriageResult
	r := withURLParams(newRequest("POST", "/pm/triage/"+id, pmPageRequest{Scope: scope}), "id", testWorkspaceID, "issueId", id)
	testutil.Call(t, testHandler.NotifyYixiezuoPMTriage, r).Want(200).JSON(&out)
	return out
}

func TestPMSyncCreatesOnceAndPreservesHumanDescription(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	if first.State != "created" || first.Assignee == "未分配" {
		t.Fatalf("first=%+v", first)
	}
	body := "人工后来写的完整正文\r\n不能覆盖\n"
	dbfx.Exec(t, `UPDATE issue SET description=$2 WHERE id=$1`, first.IssueID, body)
	item.Title, item.Priority, item.ObservedAt = "Changed source title", "低", time.Now().UTC()
	second := syncPM(t, scope, item)
	if second.State != "updated" || second.IssueID != first.IssueID {
		t.Fatalf("second=%+v", second)
	}
	var description, priority, status string
	var assignee string
	dbfx.QueryRow(t, `SELECT description, priority, status, assignee_id FROM issue WHERE id=$1`, first.IssueID).Scan(&description, &priority, &status, &assignee)
	if !strings.HasPrefix(description, body) || strings.Count(description, "外部单号：12345") != 1 || priority != "low" || status != "backlog" || assignee != testUserID {
		t.Fatalf("description=%q priority=%s status=%s assignee=%s", description, priority, status, assignee)
	}
	third := syncPM(t, scope, item)
	if third.State != "unchanged" || len(third.Changes) != 0 {
		t.Fatalf("repeat=%+v", third)
	}
	if dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE workspace_id=$1 AND external_id=$2`, testWorkspaceID, item.Source.ID) != 1 {
		t.Fatal("duplicate import")
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE workspace_id=$1`, testWorkspaceID); count != 0 {
		var requestKey, requirementID, kind, issueID, externalID string
		dbfx.QueryRow(t, `SELECT coalesce(request_key, ''), coalesce(requirement_id::text, ''), kind, coalesce(issue_id::text, ''), coalesce(payload->'source'->>'id', '') FROM yixiezuo_operation WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 1`, testWorkspaceID).Scan(&requestKey, &requirementID, &kind, &issueID, &externalID)
		t.Fatalf("PM queued a source operation: count=%d latest request_key=%q requirement_id=%q kind=%s issue_id=%s external_id=%s", count, requestKey, requirementID, kind, issueID, externalID)
	}
}

func TestPMSyncAdoptsLegacyMarkersAndTitleMatches(t *testing.T) {
	for _, marker := range []bool{true, false} {
		t.Run(map[bool]string{true: "marker", false: "title"}[marker], func(t *testing.T) {
			scope, item := pmFixture(t)
			title, description := item.Title, "人工旧正文"
			if marker {
				title = "renamed locally"
				description += "\n外部单号：12345"
			}
			id := dbfx.Issue(t, title, testutil.Cols{"project_id": scope.ProjectID, "description": description, "status": "todo"})
			out := syncPM(t, scope, item)
			if out.State == "failed" || out.State == "created" || out.IssueID != id {
				t.Fatalf("legacy=%+v", out)
			}
		})
	}
}

func TestPMSyncConcurrentRetriesAndAmbiguousLegacy(t *testing.T) {
	scope, item := pmFixture(t)
	var wg sync.WaitGroup
	responses := make(chan yixiezuo.PMResult, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := withURLParam(newRequest("POST", "/pm/issues", yixiezuo.PMSourceRequest{Scope: scope, Issue: item}), "id", testWorkspaceID)
			testHandler.SyncYixiezuoPMIssue(w, r)
			var out yixiezuo.PMResult
			json.Unmarshal(w.Body.Bytes(), &out)
			responses <- out
		}()
	}
	wg.Wait()
	close(responses)
	id := ""
	created := 0
	for out := range responses {
		if out.State == "failed" {
			t.Fatalf("concurrent=%+v", out)
		}
		if out.State == "created" {
			created++
		}
		if id != "" && id != out.IssueID {
			t.Fatal("duplicate issue ids")
		}
		id = out.IssueID
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	item.Source, _ = yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/12346")
	for i := 0; i < 2; i++ {
		dbfx.Issue(t, "ambiguous", testutil.Cols{"project_id": scope.ProjectID, "description": "外部单号：12346"})
	}
	if out := syncPM(t, scope, item); out.State != "failed" || !strings.Contains(out.Error, "multiple") {
		t.Fatalf("ambiguous=%+v", out)
	}
}

func TestPMSyncUnmappedOwnerAndExplicitMapping(t *testing.T) {
	scope, item := pmFixture(t)
	item.Assignee.Email = "unmapped@corp.netease.com"
	scope.AssigneeEmails = []string{item.Assignee.Email}
	first := syncPM(t, scope, item)
	if first.State != "created" || first.Assignee != "未分配" || len(first.Warnings) == 0 {
		t.Fatalf("unmapped=%+v", first)
	}
	scope.AssigneeMap = map[string]string{item.Assignee.Email: testUserID}
	item.ObservedAt = time.Now().UTC()
	if second := syncPM(t, scope, item); second.Assignee == "未分配" || second.State != "updated" {
		t.Fatalf("mapping=%+v", second)
	}
}

func TestPMSyncReconcilesClosedSourceOutsideDiscoveryAndRejectsStaleReads(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	var sources struct {
		Sources []yixiezuo.Source `json:"sources"`
	}
	testutil.Call(t, testHandler.ListYixiezuoPMReconcileSources, withURLParam(newRequest("POST", "/pm/reconcile", pmPageRequest{Scope: scope}), "id", testWorkspaceID)).Want(200).JSON(&sources)
	if len(sources.Sources) != 1 || sources.Sources[0] != item.Source {
		t.Fatalf("sources=%+v", sources)
	}
	old := item
	item.Status = "已关闭"
	item.Assignee.Email = "moved@corp.netease.com"
	item.ObservedAt = time.Now().UTC()
	closed := syncPM(t, scope, item)
	if closed.State != "updated" || closed.IssueID != first.IssueID {
		t.Fatalf("close=%+v", closed)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, first.IssueID).Scan(&status)
	if status != "done" {
		t.Fatalf("status=%s", status)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND type='status_change'`, first.IssueID); count != 1 {
		t.Fatalf("closure comments=%d", count)
	}
	syncPM(t, scope, item)
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND type='status_change'`, first.IssueID); count != 1 {
		t.Fatalf("repeated closure comments=%d", count)
	}
	if out := syncPM(t, scope, old); out.State != "failed" || !strings.Contains(out.Error, "stale") {
		t.Fatalf("stale=%+v", out)
	}
}

func TestPMSyncReconcilesManualBindingAfterSourceOwnerChanged(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	// A single import has no PM observation, but retains the source binding.
	dbfx.Exec(t, `UPDATE yixiezuo_import SET pm_state='{}'::jsonb WHERE issue_id=$1`, first.IssueID)
	item.Status, item.IssueType, item.Assignee.Email = "已取消", "Requirement", "moved@corp.netease.com"
	item.ObservedAt = time.Now().UTC()
	closed := syncPM(t, scope, item)
	if closed.State != "updated" || closed.IssueID != first.IssueID {
		t.Fatalf("legacy closure=%+v", closed)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, first.IssueID).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("status=%s", status)
	}
}

func TestPMTriageDedupAndFailureRetryReuseComment(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "待分类", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	agentID, _ := scope.TriageAgentID()
	dbfx.Exec(t, `UPDATE agent SET archived_at=now() WHERE id=$1`, agentID)
	failed := notifyPM(t, scope, id)
	if failed.State != "failed" || failed.ReasonCode != "target_unavailable" || failed.CommentID == "" {
		t.Fatalf("failed=%+v", failed)
	}
	dbfx.Exec(t, `UPDATE agent SET archived_at=NULL WHERE id=$1`, agentID)
	success := notifyPM(t, scope, id)
	if success.State != "notified" || success.CommentID != failed.CommentID {
		t.Fatalf("retry=%+v", success)
	}
	repeated := notifyPM(t, scope, id)
	if repeated.State != "already_notified" || repeated.CommentID != failed.CommentID {
		t.Fatalf("repeat=%+v", repeated)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("comments=%d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("tasks=%d", count)
	}
	var content, status string
	var assignee *string
	dbfx.QueryRow(t, `SELECT content FROM comment WHERE id=$1`, failed.CommentID).Scan(&content)
	dbfx.QueryRow(t, `SELECT status,assignee_id FROM issue WHERE id=$1`, id).Scan(&status, &assignee)
	if !strings.HasPrefix(content, scope.TriageMention) || status != "backlog" || assignee != nil {
		t.Fatalf("content=%q status=%s assignee=%v", content, status, assignee)
	}
	// A crash after dispatch but before saving its receipt must not re-run a
	// completed task, even when the next wake happens much later.
	dbfx.Exec(t, `UPDATE yixiezuo_pm_notification SET state='pending' WHERE issue_id=$1`, id)
	if recovered := notifyPM(t, scope, id); recovered.State != "notified" {
		t.Fatalf("recover=%+v", recovered)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("recovery duplicated tasks=%d", count)
	}
}

func TestPMTriageSkipsHumanHandledAndMappedIssues(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "已人工讨论", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	dbfx.Comment(t, id, "人工已处理")
	if out := notifyPM(t, scope, id); out.State != "skipped" {
		t.Fatalf("human=%+v", out)
	}
	assigned := dbfx.Issue(t, "已有负责人", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog", "assignee_type": "member", "assignee_id": testUserID})
	if out := notifyPM(t, scope, assigned); out.State != "skipped" {
		t.Fatalf("assigned=%+v", out)
	}
}

func TestPMSyncAcceptsAgentWithoutOpeningManualPublication(t *testing.T) {
	scope, item := pmFixture(t)
	agentID, _ := scope.TriageAgentID()
	task := dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})
	r := withURLParam(newRequest("POST", "/pm/issues", yixiezuo.PMSourceRequest{Scope: scope, Issue: item}), "id", testWorkspaceID)
	r.Header.Set("X-Agent-ID", agentID)
	r.Header.Set("X-Task-ID", task)
	var out yixiezuo.PMResult
	testutil.Call(t, testHandler.SyncYixiezuoPMIssue, r).Want(http.StatusOK).JSON(&out)
	if out.State != "created" {
		t.Fatalf("agent=%+v", out)
	}
	preview := withURLParam(newRequest("POST", "/preview", map[string]string{"url": item.Source.URL}), "id", testWorkspaceID)
	preview.Header.Set("X-Agent-ID", agentID)
	preview.Header.Set("X-Task-ID", task)
	testutil.Call(t, testHandler.PreviewYixiezuoIssue, preview).Want(http.StatusForbidden)
}

func TestPMSyncKeepsLocalRoutingWhenSourceHasNotChanged(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	agent, _ := scope.TriageAgentID()
	dbfx.Exec(t, `UPDATE issue SET title='人工调整标题', priority='urgent', assignee_type='agent', assignee_id=$2 WHERE id=$1`, first.IssueID, agent)
	item.ObservedAt = time.Now().UTC()
	repeated := syncPM(t, scope, item)
	if repeated.State != "unchanged" {
		t.Fatalf("repeat=%+v", repeated)
	}
	var title, priority, ownerType, owner string
	dbfx.QueryRow(t, `SELECT title, priority, assignee_type, assignee_id FROM issue WHERE id=$1`, first.IssueID).Scan(&title, &priority, &ownerType, &owner)
	if title != "人工调整标题" || priority != "urgent" || ownerType != "agent" || owner != agent {
		t.Fatalf("title=%s priority=%s owner=%s:%s", title, priority, ownerType, owner)
	}
}

func TestPMSyncAcceptsMinutePrecisionAfterDetailedObservation(t *testing.T) {
	scope, item := pmFixture(t)
	item.UpdatedAt = "2026-10-14T12:00:45Z"
	first := syncPM(t, scope, item)
	item.Title, item.UpdatedAt, item.UpdatedAtMinutePrecision = "Source edit in the same minute", "2026-10-14T12:00:00Z", true
	item.ObservedAt = time.Now().UTC()
	if out := syncPM(t, scope, item); out.State != "updated" || out.IssueID != first.IssueID {
		t.Fatalf("minute precision=%+v", out)
	}
	item.UpdatedAt, item.UpdatedAtMinutePrecision = "2026-10-14T11:59:59Z", false
	item.ObservedAt = time.Now().UTC()
	if out := syncPM(t, scope, item); out.State != "failed" || !strings.Contains(out.Error, "older") {
		t.Fatalf("older version=%+v", out)
	}
}

func TestPMSyncMappingChangesRespectPolymorphicLocalOwner(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	// Users and agents have independent UUID namespaces. An identical UUID
	// does not make an agent assignment the old mapped member assignment.
	agent := dbfx.Agent(t, "Local agent with member UUID", testRuntimeID, testutil.Cols{"id": testUserID})
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, first.IssueID, agent)
	member := dbfx.User(t, "New mapped member", "mapped-"+scope.ProjectID+"@corp.netease.com")
	dbfx.Member(t, testWorkspaceID, member, "member")
	scope.AssigneeMap = map[string]string{item.Assignee.Email: member}
	item.ObservedAt = time.Now().UTC()
	if out := syncPM(t, scope, item); out.State != "unchanged" {
		t.Fatalf("mapping undid local routing: %+v", out)
	}
	var kind, owner string
	dbfx.QueryRow(t, `SELECT assignee_type,assignee_id::text FROM issue WHERE id=$1`, first.IssueID).Scan(&kind, &owner)
	if kind != "agent" || owner != agent {
		t.Fatalf("owner=%s:%s", kind, owner)
	}
}

func TestPMSyncDeletedBindingAndForeignSourceLinkFailExplicitly(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	testutil.Call(t, testHandler.DeleteIssue, withURLParam(newRequest("DELETE", "/issues/"+first.IssueID, nil), "id", first.IssueID)).Want(204)
	item.ObservedAt = time.Now().UTC()
	for i := 0; i < 2; i++ {
		if out := syncPM(t, scope, item); out.State != "failed" || !strings.Contains(out.Error, "no longer exists") {
			t.Fatalf("deleted=%+v", out)
		}
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1 AND project_id=$2`, testWorkspaceID, scope.ProjectID); count != 0 {
		t.Fatalf("rebuilt deleted binding: %d", count)
	}
	item.Source, _ = yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/54321")
	dbfx.Issue(t, item.Title, testutil.Cols{"project_id": scope.ProjectID, "description": "外部单号：54321\n来源链接：https://other.pm.netease.com/issues/54321"})
	if out := syncPM(t, scope, item); out.State != "failed" || !strings.Contains(out.Error, "source link") {
		t.Fatalf("foreign=%+v", out)
	}
}

func TestPMTriageConcurrentCyclesNotifyOnce(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "并发待分诊", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	var wg sync.WaitGroup
	results := make(chan yixiezuo.PMTriageResult, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := withURLParams(newRequest("POST", "/pm/triage/"+id, pmPageRequest{Scope: scope}), "id", testWorkspaceID, "issueId", id)
			testHandler.NotifyYixiezuoPMTriage(w, r)
			var out yixiezuo.PMTriageResult
			json.Unmarshal(w.Body.Bytes(), &out)
			results <- out
		}()
	}
	wg.Wait()
	close(results)
	for out := range results {
		if out.State != "notified" && out.State != "already_notified" {
			t.Fatalf("concurrent=%+v", out)
		}
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("comments=%d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("tasks=%d", count)
	}
}

func TestPMTriageRejectsForeignTargetWithoutNotificationRows(t *testing.T) {
	scope, _ := pmFixture(t)
	foreign := dbfx.Workspace(t, "Foreign PM triage workspace", "foreign-pm-"+scope.ProjectID)
	runtime := dbfx.Runtime(t, "Foreign PM triage runtime", testutil.Cols{"workspace_id": foreign})
	agent := dbfx.Agent(t, "Foreign PM triager", runtime, testutil.Cols{"workspace_id": foreign})
	scope.TriageMention = "[@任务单分诊员](mention://agent/" + agent + ")"
	id := dbfx.Issue(t, "Foreign triager rejected", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	out := notifyPM(t, scope, id)
	if out.State != "failed" || out.ReasonCode == "" {
		t.Fatalf("foreign target=%+v", out)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_pm_notification WHERE issue_id=$1`, id); count != 0 {
		t.Fatalf("invalid agent relationship persisted: %d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, id); count != 0 {
		t.Fatalf("foreign target comment posted: %d", count)
	}
}

func TestPMSyncScopeChangeKeepsExistingBaseline(t *testing.T) {
	for _, project := range []string{"10", "DJ01单机-Bug"} {
		t.Run(project, func(t *testing.T) {
			scope, item := pmFixture(t)
			first := syncPM(t, scope, item)
			if first.State != "created" {
				t.Fatalf("create=%+v", first)
			}
			agent, _ := scope.TriageAgentID()
			dbfx.Exec(t, `UPDATE issue SET title='Local title', priority='urgent', assignee_type='agent', assignee_id=$2 WHERE id=$1`, first.IssueID, agent)
			scope.SourceProject, item.ObservedAt = project, time.Now().UTC()
			out := syncPM(t, scope, item)
			if out.State != "unchanged" || len(out.Changes) != 0 {
				t.Fatalf("scope change overwrote local routing: %+v", out)
			}
			var title, priority, kind, owner string
			dbfx.QueryRow(t, `SELECT title, priority, assignee_type, assignee_id::text FROM issue WHERE id=$1`, first.IssueID).Scan(&title, &priority, &kind, &owner)
			if title != "Local title" || priority != "urgent" || kind != "agent" || owner != agent {
				t.Fatalf("title=%q priority=%s owner=%s:%s", title, priority, kind, owner)
			}
			item.Title, item.ObservedAt = "New source title", time.Now().UTC()
			if changed := syncPM(t, scope, item); changed.State != "updated" {
				t.Fatalf("source edit after scope change=%+v", changed)
			}
		})
	}
}

func TestPMSyncAdoptionPreservesLocalOwner(t *testing.T) {
	for _, binding := range []bool{false, true} {
		for _, kind := range []string{"member", "agent"} {
			for _, mapped := range []bool{false, true} {
				name := fmt.Sprintf("binding=%t/%s/mapped=%t", binding, kind, mapped)
				t.Run(name, func(t *testing.T) {
					scope, item := pmFixture(t)
					owner := testUserID
					if kind == "agent" {
						owner, _ = scope.TriageAgentID()
					} else {
						owner = dbfx.User(t, "Local owner", "local-owner-"+scope.ProjectID+"@multica.test")
						dbfx.Member(t, testWorkspaceID, owner, "member")
					}
					var id string
					if binding {
						first := syncPM(t, scope, item)
						if first.State != "created" {
							t.Fatalf("create=%+v", first)
						}
						id = first.IssueID
						dbfx.Exec(t, `UPDATE yixiezuo_import SET pm_state='{}'::jsonb WHERE issue_id=$1`, id)
						dbfx.Exec(t, `UPDATE issue SET assignee_type=$2, assignee_id=$3 WHERE id=$1`, id, kind, owner)
					} else {
						id = dbfx.Issue(t, item.Title, testutil.Cols{"project_id": scope.ProjectID, "description": "外部单号：12345", "assignee_type": kind, "assignee_id": owner})
					}
					if !mapped {
						item.Assignee.Email = "unmapped@corp.netease.com"
						scope.AssigneeEmails = []string{item.Assignee.Email}
					}
					item.ObservedAt = time.Now().UTC()
					if out := syncPM(t, scope, item); out.State == "failed" || out.IssueID != id {
						t.Fatalf("adopt=%+v", out)
					}
					var gotKind, gotOwner string
					dbfx.QueryRow(t, `SELECT COALESCE(assignee_type, ''), COALESCE(assignee_id::text, '') FROM issue WHERE id=$1`, id).Scan(&gotKind, &gotOwner)
					if gotKind != kind || gotOwner != owner {
						t.Fatalf("adoption replaced %s:%s with %s:%s", kind, owner, gotKind, gotOwner)
					}
				})
			}
		}
	}
}

func TestPMReconcileRequiresStandaloneExternalID(t *testing.T) {
	scope, _ := pmFixture(t)
	for _, body := range []string{"参考外部单号：12345", "外部单号：12345 的相关资料", "外部单号：\n12345"} {
		dbfx.Issue(t, body, testutil.Cols{"project_id": scope.ProjectID, "description": body})
	}
	dbfx.Issue(t, "Legacy CRLF", testutil.Cols{"project_id": scope.ProjectID, "description": "正文\r\n \t外部单号: 42 \t\r\n下一行"})
	dbfx.Issue(t, "Conflicting markers", testutil.Cols{"project_id": scope.ProjectID, "description": "外部单号：43\n外部单号：44"})
	var out struct {
		Sources []yixiezuo.Source   `json:"sources"`
		Errors  []yixiezuo.PMResult `json:"errors"`
	}
	testutil.Call(t, testHandler.ListYixiezuoPMReconcileSources, withURLParam(newRequest("POST", "/pm/reconcile", pmPageRequest{Scope: scope}), "id", testWorkspaceID)).Want(200).JSON(&out)
	if len(out.Sources) != 1 || out.Sources[0].ID != "42" || len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Error, "冲突") {
		t.Fatalf("reconcile=%+v", out)
	}
}

func TestPMSyncDeletedIssueCanBeExplicitlyReimported(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	if first.State != "created" {
		t.Fatalf("create=%+v", first)
	}
	testutil.Call(t, testHandler.DeleteIssue, withURLParam(newRequest("DELETE", "/issues/"+first.IssueID, nil), "id", first.IssueID)).Want(204)
	if blocked := syncPM(t, scope, item); blocked.State != "failed" {
		t.Fatalf("PM recreated deleted issue: %+v", blocked)
	}
	var preview yixiezuo.Operation
	testutil.Call(t, testHandler.PreviewYixiezuoIssue, yixiezuoRequest("POST", "/preview", map[string]any{"url": item.Source.URL})).Want(201).JSON(&preview)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM yixiezuo_operation WHERE id=$1`, preview.ID) })
	var claimed struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&claimed)
	snapshot := yixiezuo.Snapshot{Source: item.Source, Title: item.Title, Description: item.Description, Status: item.Status, Priority: item.Priority,
		Attachments: []yixiezuo.Attachment{}, Comments: []yixiezuo.Comment{}, Fields: []yixiezuo.SourceField{}, Statuses: []yixiezuo.Status{}, Warnings: []string{}}
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", preview.ID, yixiezuo.Completion{LeaseToken: claimed.Operation.LeaseToken, State: "succeeded", Snapshot: &snapshot})).Want(200)
	invalid := map[string]any{"operation_id": preview.ID, "project_id": "00000000-0000-4000-8000-000000000099"}
	testutil.Call(t, testHandler.ImportYixiezuoIssue, yixiezuoRequest("POST", "/imports", invalid)).Want(400)
	if count := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id=$2 AND pm_state ? 'deleted_at'`, testWorkspaceID, first.IssueID); count != 1 {
		t.Fatalf("failed explicit import removed deletion receipt: %d", count)
	}
	body := map[string]any{"operation_id": preview.ID, "project_id": scope.ProjectID}
	var imported struct {
		Issue    IssueResponse `json:"issue"`
		Existing bool          `json:"existing"`
	}
	testutil.Call(t, testHandler.ImportYixiezuoIssue, yixiezuoRequest("POST", "/imports", body)).Want(200).JSON(&imported)
	if imported.Existing || imported.Issue.ID == first.IssueID || imported.Issue.ID == "" {
		t.Fatalf("explicit reimport=%+v", imported)
	}
	var repeated struct {
		Issue    IssueResponse `json:"issue"`
		Existing bool          `json:"existing"`
	}
	testutil.Call(t, testHandler.ImportYixiezuoIssue, yixiezuoRequest("POST", "/imports", body)).Want(200).JSON(&repeated)
	if !repeated.Existing || repeated.Issue.ID != imported.Issue.ID {
		t.Fatalf("repeated import=%+v", repeated)
	}
	item.ObservedAt = time.Now().UTC()
	if adopted := syncPM(t, scope, item); adopted.State == "failed" || adopted.IssueID != imported.Issue.ID {
		t.Fatalf("PM after explicit reimport=%+v", adopted)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE workspace_id=$1 AND source_host=$2 AND external_id=$3`, testWorkspaceID, item.Source.Host, item.Source.ID); count != 1 {
		t.Fatalf("source bindings=%d", count)
	}
}

func TestPMTriageRetryByAnotherAuthorizedMember(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "Retry by another administrator", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	agent, _ := scope.TriageAgentID()
	dbfx.Exec(t, `UPDATE agent SET archived_at=now() WHERE id=$1`, agent)
	first := notifyPM(t, scope, id)
	if first.State != "failed" || first.CommentID == "" {
		t.Fatalf("first=%+v", first)
	}
	dbfx.Exec(t, `UPDATE agent SET archived_at=NULL WHERE id=$1`, agent)
	member := dbfx.User(t, "Retry administrator", "retry-"+scope.ProjectID+"@multica.test")
	dbfx.Member(t, testWorkspaceID, member, "admin")
	r := withURLParams(newRequestAs(member, "POST", "/pm/triage/"+id, pmPageRequest{Scope: scope}), "id", testWorkspaceID, "issueId", id)
	var retried yixiezuo.PMTriageResult
	testutil.Call(t, testHandler.NotifyYixiezuoPMTriage, r).Want(200).JSON(&retried)
	if retried.State != "notified" || retried.CommentID != first.CommentID {
		t.Fatalf("retry=%+v", retried)
	}
	var originator, author string
	dbfx.QueryRow(t, `SELECT originator_user_id::text FROM agent_task_queue WHERE issue_id=$1`, id).Scan(&originator)
	dbfx.QueryRow(t, `SELECT author_id::text FROM comment WHERE id=$1`, first.CommentID).Scan(&author)
	if originator != testUserID || author != testUserID {
		t.Fatalf("retry changed original comment attribution: originator=%s author=%s", originator, author)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, id); count != 1 {
		t.Fatalf("retry duplicated comments: %d", count)
	}
}

func TestPMTriageRetryDoesNotBorrowOriginalInvokeRights(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "Unauthorized retry", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	agent, _ := scope.TriageAgentID()
	dbfx.Exec(t, `UPDATE agent SET permission_mode='private', archived_at=now() WHERE id=$1`, agent)
	first := notifyPM(t, scope, id)
	if first.State != "failed" || first.CommentID == "" {
		t.Fatalf("first=%+v", first)
	}
	dbfx.Exec(t, `UPDATE agent SET archived_at=NULL WHERE id=$1`, agent)
	member := dbfx.User(t, "Unauthorized administrator", "unauthorized-"+scope.ProjectID+"@multica.test")
	dbfx.Member(t, testWorkspaceID, member, "admin")
	r := withURLParams(newRequestAs(member, "POST", "/pm/triage/"+id, pmPageRequest{Scope: scope}), "id", testWorkspaceID, "issueId", id)
	var denied yixiezuo.PMTriageResult
	testutil.Call(t, testHandler.NotifyYixiezuoPMTriage, r).Want(200).JSON(&denied)
	if denied.State != "failed" || denied.ReasonCode != "invocation_not_allowed" || denied.CommentID != "" {
		t.Fatalf("unauthorized retry=%+v", denied)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, id); count != 0 {
		t.Fatalf("unauthorized retry queued tasks: %d", count)
	}
}

func TestPMTriageAgentRetryPreservesOriginalTaskLineage(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "Agent retry with another originator", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	triager, _ := scope.TriageAgentID()
	pm := createHandlerTestAgent(t, "PM retry caller "+scope.ProjectID, nil)
	member := dbfx.User(t, "Next PM originator", "pm-retry-"+scope.ProjectID+"@multica.test")
	dbfx.Member(t, testWorkspaceID, member, "admin")
	var first yixiezuo.PMTriageResult
	var originalTask string
	dbfx.Exec(t, `UPDATE agent SET archived_at=now() WHERE id=$1`, triager)
	for i, originator := range []string{testUserID, member} {
		task := dbfx.Task(t, pm, testutil.Cols{"status": "running", "runtime_id": testRuntimeID, "originator_user_id": originator, "accountable_user_id": originator})
		r := withURLParams(newRequest("POST", "/pm/triage/"+id, pmPageRequest{Scope: scope}), "id", testWorkspaceID, "issueId", id)
		r.Header.Set("X-Agent-ID", pm)
		r.Header.Set("X-Task-ID", task)
		var out yixiezuo.PMTriageResult
		testutil.Call(t, testHandler.NotifyYixiezuoPMTriage, r).Want(200).JSON(&out)
		if i == 0 {
			if out.State != "failed" || out.CommentID == "" {
				t.Fatalf("first agent invocation=%+v", out)
			}
			first, originalTask = out, task
			dbfx.Exec(t, `UPDATE agent SET archived_at=NULL WHERE id=$1`, triager)
		} else if out.State != "notified" || out.CommentID != first.CommentID {
			t.Fatalf("agent retry=%+v", out)
		}
	}
	var originator, accountable, delegatedFrom, author, sourceTask string
	dbfx.QueryRow(t, `SELECT originator_user_id::text, accountable_user_id::text, delegated_from_task_id::text FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2`, id, triager).Scan(&originator, &accountable, &delegatedFrom)
	dbfx.QueryRow(t, `SELECT author_id::text, source_task_id::text FROM comment WHERE id=$1`, first.CommentID).Scan(&author, &sourceTask)
	if originator != testUserID || accountable != testUserID || delegatedFrom != originalTask || author != pm || sourceTask != originalTask {
		t.Fatalf("retry changed lineage: originator=%s accountable=%s delegated=%s author=%s source_task=%s", originator, accountable, delegatedFrom, author, sourceTask)
	}
}

func TestPMTriageChecksConcurrencyBeforeCommenting(t *testing.T) {
	scope, _ := pmFixture(t)
	id := dbfx.Issue(t, "Misconfigured triager", testutil.Cols{"project_id": scope.ProjectID, "status": "backlog"})
	agent, _ := scope.TriageAgentID()
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks=6 WHERE id=$1`, agent)
	out := notifyPM(t, scope, id)
	if out.State != "failed" || out.ReasonCode != "triage_concurrency_must_be_one" || out.CommentID != "" {
		t.Fatalf("concurrency check=%+v", out)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, id); count != 0 {
		t.Fatalf("misconfiguration posted comments: %d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_pm_notification WHERE issue_id=$1`, id); count != 0 {
		t.Fatalf("misconfiguration persisted notifications: %d", count)
	}
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks=1 WHERE id=$1`, agent)
	if fixed := notifyPM(t, scope, id); fixed.State != "notified" {
		t.Fatalf("after fixing concurrency=%+v", fixed)
	}
}

func TestPMSyncClosureEventsUseFinalRevision(t *testing.T) {
	scope, item := pmFixture(t)
	first := syncPM(t, scope, item)
	if first.State != "created" {
		t.Fatalf("create=%+v", first)
	}
	var seen []events.Event
	h := *testHandler
	h.Bus = events.New()
	h.Bus.SubscribeAll(func(e events.Event) { seen = append(seen, e) })
	item.Status, item.ObservedAt = "已关闭", time.Now().UTC()
	r := withURLParam(newRequest("POST", "/pm/issues", yixiezuo.PMSourceRequest{Scope: scope, Issue: item}), "id", testWorkspaceID)
	var out yixiezuo.PMResult
	testutil.Call(t, h.SyncYixiezuoPMIssue, r).Want(200).JSON(&out)
	if out.State != "updated" || len(seen) != 2 || seen[0].Type != protocol.EventIssueUpdated || seen[1].Type != protocol.EventCommentCreated {
		t.Fatalf("closure=%+v events=%+v", out, seen)
	}
	issue := seen[0].Payload.(map[string]any)["issue"].(IssueResponse)
	evidence := seen[1].Payload.(map[string]any)
	if revision := evidence["issue_revision"]; revision != issue.Revision || evidence["comment"].(CommentResponse).IssueRevision != issue.Revision {
		t.Fatalf("closure revisions moved backwards: issue=%d evidence=%v", issue.Revision, revision)
	}
}

func TestPMSyncMetadataChangesHaveStableOrder(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			scope, item := pmFixture(t)
			dbfx.Issue(t, item.Title, testutil.Cols{"project_id": scope.ProjectID, "description": "外部单号：12345", "priority": "high", "assignee_type": "member", "assignee_id": testUserID})
			out := syncPM(t, scope, item)
			want := []string{"来源链接", "来源关联", "易协作状态", "易协作优先级"}
			if out.State != "updated" || len(out.Changes) != len(want) {
				t.Fatalf("metadata changes=%+v", out)
			}
			for i, field := range want {
				if out.Changes[i].Field != field {
					t.Fatalf("change %d=%q; want %q", i, out.Changes[i].Field, field)
				}
			}
		})
	}
}
