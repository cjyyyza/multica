package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http"
	"testing"
)

func yixiezuoRequest(method, path string, body any) *http.Request {
	return withURLParam(newRequest(method, path, body), "id", testWorkspaceID)
}
func manualOperationRequest(method, operationID string, body any) *http.Request {
	return withURLParam(yixiezuoRequest(method, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/operations/"+operationID, body), "operationId", operationID)
}
func manualIssueRequest(method, issueID string, body any) *http.Request {
	return withURLParam(yixiezuoRequest(method, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/imports/"+issueID, body), "issueId", issueID)
}
func prepareManualImport(t *testing.T) (string, IssueResponse) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_operation WHERE workspace_id=$1`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_import WHERE workspace_id=$1`, testWorkspaceID)
	})
	var queued yixiezuo.Operation
	testutil.Call(t, testHandler.PreviewYixiezuoIssue, yixiezuoRequest("POST", "/preview", map[string]any{"url": "https://dj01.pm.netease.com/issues/12"})).Want(201).JSON(&queued)
	var count int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM yixiezuo_import WHERE workspace_id=$1`, testWorkspaceID).Scan(&count)
	if count != 0 {
		t.Fatal("preview created a task")
	}
	var claimed struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&claimed)
	source, _ := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/12")
	lock := int64(1)
	snapshot := yixiezuo.Snapshot{Source: source, Title: "Source bug", Description: "Repro and expected behavior", Status: "Ready", LockVersion: &lock, Attachments: []yixiezuo.Attachment{}, Comments: []yixiezuo.Comment{}, Fields: []yixiezuo.SourceField{}, Statuses: []yixiezuo.Status{}, Warnings: []string{}}
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", queued.ID, yixiezuo.Completion{LeaseToken: claimed.Operation.LeaseToken, State: "succeeded", Snapshot: &snapshot})).Want(200)
	var imported struct {
		Issue    IssueResponse `json:"issue"`
		Existing bool          `json:"existing"`
	}
	testutil.Call(t, testHandler.ImportYixiezuoIssue, yixiezuoRequest("POST", "/imports", map[string]any{"operation_id": queued.ID})).Want(200).JSON(&imported)
	if imported.Existing || imported.Issue.AssigneeID != nil {
		t.Fatal("new import must be unassigned")
	}
	return queued.ID, imported.Issue
}

func TestYixiezuoSingleImportIsExplicitAndIdempotent(t *testing.T) {
	operation, first := prepareManualImport(t)
	var second struct {
		Issue    IssueResponse `json:"issue"`
		Existing bool          `json:"existing"`
	}
	testutil.Call(t, testHandler.ImportYixiezuoIssue, yixiezuoRequest("POST", "/imports", map[string]any{"operation_id": operation})).Want(200).JSON(&second)
	if !second.Existing || second.Issue.ID != first.ID {
		t.Fatalf("duplicate import %+v", second)
	}
	var count int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, first.ID).Scan(&count)
	if count != 0 {
		t.Fatal("import started an agent")
	}
}

func TestYixiezuoPublicationKeepsTheSubmittedRevision(t *testing.T) {
	_, issue := prepareManualImport(t)
	var digest string
	testPool.QueryRow(context.Background(), `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue.ID).Scan(&digest)
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, map[string]any{"summary": "tested", "revision": issue.Revision, "source_digest": digest})).Want(400)
	var op yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, map[string]any{"summary": "tested", "revision": issue.Revision, "confirmed": true, "source_digest": digest})).Want(201).JSON(&op)
	var claimed struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&claimed)
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"title": "New local edit"}), "id", issue.ID)).Want(200)
	var raw []byte
	testPool.QueryRow(context.Background(), `SELECT snapshot FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue.ID).Scan(&raw)
	var snapshot yixiezuo.Snapshot
	json.Unmarshal(raw, &snapshot)
	receipt := yixiezuo.Completion{LeaseToken: claimed.Operation.LeaseToken, State: "succeeded", Snapshot: &snapshot}
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", op.ID, receipt)).Want(200)
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", op.ID, receipt)).Want(200)
	var details struct {
		Import struct {
			State             string `json:"state"`
			PublishedRevision int64  `json:"published_revision"`
		} `json:"import"`
	}
	testutil.Call(t, testHandler.GetYixiezuoImport, manualIssueRequest("GET", issue.ID, nil)).Want(200).JSON(&details)
	if details.Import.State != "needs_review" || details.Import.PublishedRevision != issue.Revision {
		t.Fatalf("new edit swallowed by old receipt: %+v", details)
	}
}
