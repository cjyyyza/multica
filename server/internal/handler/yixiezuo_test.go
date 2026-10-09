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
	return withURLParams(newRequest(method, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/operations/"+operationID, body), "id", testWorkspaceID, "operationId", operationID)
}
func manualIssueRequest(method, issueID string, body any) *http.Request {
	return withURLParams(newRequest(method, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/imports/"+issueID, body), "id", testWorkspaceID, "issueId", issueID)
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
	// The evidence comment is part of the publication, so the queued revision
	// includes that activity. A later local edit must still stay outside it.
	var atQueue int64
	if err := testPool.QueryRow(context.Background(), `SELECT revision FROM issue WHERE id = $1`, issue.ID).Scan(&atQueue); err != nil {
		t.Fatal(err)
	}
	if atQueue <= issue.Revision {
		t.Fatalf("queued revision %d, reviewed %d", atQueue, issue.Revision)
	}
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
	if details.Import.State != "needs_review" || details.Import.PublishedRevision != atQueue {
		t.Fatalf("new edit swallowed by old receipt: %+v", details)
	}
}

func TestYixiezuoPublishRecordsEvidenceComment(t *testing.T) {
	_, issue := prepareManualImport(t)
	var digest string
	if err := testPool.QueryRow(context.Background(), `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	const swarmURL = "https://swarm.example.com/reviews/42"
	reviewID := dbfx.Insert(t, "swarm_review", testutil.Cols{
		"workspace_id": testWorkspaceID, "swarm_url": "https://swarm.example.com",
		"review_number": 42, "html_url": swarmURL,
	})
	dbfx.InsertNoID(t, "issue_swarm_review", testutil.Cols{
		"issue_id": issue.ID, "swarm_review_id": reviewID, "workspace_id": testWorkspaceID,
	}, "issue_id = $1 AND swarm_review_id = $2", issue.ID, reviewID)
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue.ID,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "shot.png", "url": "https://example.test/shot.png",
		"content_type": "image/png", "size_bytes": 1,
	})
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
	})

	var op yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, map[string]any{
		"summary": "Fixed and verified", "revision": issue.Revision, "confirmed": true,
		"source_digest": digest, "attachment_ids": []string{attachmentID},
	})).Want(201).JSON(&op)
	if op.Kind != "publish" || op.State != "pending" || op.Payload.Summary != "Fixed and verified" {
		t.Fatalf("operation %+v", op)
	}
	if dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE id = $1 AND state = 'pending'`, op.ID) != 1 {
		t.Fatal("publish was not queued")
	}

	var content, kind, authorType, authorID string
	dbfx.QueryRow(t, `SELECT content, type, author_type, author_id::text FROM comment WHERE issue_id = $1 AND type = 'comment'`, issue.ID).Scan(&content, &kind, &authorType, &authorID)
	if kind != "comment" || authorType != "member" || authorID != testUserID {
		t.Fatalf("comment author %s %s type %s", authorType, authorID, kind)
	}
	if content != "Fixed and verified\n"+swarmURL {
		t.Fatalf("content = %q", content)
	}
	if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND comment_id = (SELECT id FROM comment WHERE issue_id = $2 AND type = 'comment')`, attachmentID, issue.ID) != 1 {
		t.Fatal("attachment was not bound to the evidence comment")
	}
	if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE issue_id = $1 AND (url = $2 OR filename = $2)`, issue.ID, swarmURL) != 0 {
		t.Fatal("swarm URL was stored as an attachment")
	}
}

func TestYixiezuoPublishRejectsBadAttachmentBeforeQueue(t *testing.T) {
	_, issue := prepareManualImport(t)
	var digest string
	if err := testPool.QueryRow(context.Background(), `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	body := func(ids []string) map[string]any {
		return map[string]any{
			"summary": "Fixed and verified", "revision": issue.Revision, "confirmed": true,
			"source_digest": digest, "attachment_ids": ids,
		}
	}
	pending := func() int {
		t.Helper()
		return dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE workspace_id = $1 AND issue_id = $2 AND kind = 'publish' AND state = 'pending'`, testWorkspaceID, issue.ID)
	}
	evidence := func() int {
		t.Helper()
		return dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE 'Fixed and verified%'`, issue.ID)
	}

	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, body([]string{"not-a-uuid"}))).Want(400)
	if pending() != 0 || evidence() != 0 {
		t.Fatal("malformed attachment id queued a publish")
	}

	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, body([]string{"22222222-2222-4222-8222-222222222222"}))).Want(409)
	if pending() != 0 || evidence() != 0 {
		t.Fatal("missing attachment id queued a publish")
	}

	foreign := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue.ID,
		"uploader_type": "member", "uploader_id": "11111111-1111-4111-8111-111111111111",
		"filename": "other.png", "url": "https://example.test/other.png",
		"content_type": "image/png", "size_bytes": 1,
	})
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, body([]string{foreign}))).Want(409)
	if pending() != 0 || evidence() != 0 {
		t.Fatal("another member's attachment queued a publish")
	}
	if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND comment_id IS NULL`, foreign) != 1 {
		t.Fatal("foreign attachment was bound")
	}

	earlier := dbfx.Comment(t, issue.ID, "earlier note")
	bound := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue.ID, "comment_id": earlier,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "bound.png", "url": "https://example.test/bound.png",
		"content_type": "image/png", "size_bytes": 1,
	})
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, body([]string{bound}))).Want(409)
	if pending() != 0 || evidence() != 0 {
		t.Fatal("already-bound attachment queued a publish")
	}
	if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND comment_id = $2`, bound, earlier) != 1 {
		t.Fatal("already-bound attachment was moved")
	}
}
