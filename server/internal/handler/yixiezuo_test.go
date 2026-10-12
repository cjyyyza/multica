package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
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

	agentID := dbfx.Agent(t, "Evidence chat agent", testRuntimeID)
	chatID := dbfx.ChatSession(t, agentID)
	otherIssue := dbfx.Issue(t, "Other evidence owner")
	otherWorkspace := dbfx.Workspace(t, "Other evidence workspace", "evidence-boundary-"+issue.ID)
	for _, tc := range []struct {
		name  string
		owner testutil.Cols
	}{
		{"other uploader", testutil.Cols{"uploader_id": "11111111-1111-4111-8111-111111111111"}},
		{"other issue", testutil.Cols{"issue_id": otherIssue}},
		{"other workspace", testutil.Cols{"workspace_id": otherWorkspace}},
		{"chat upload", testutil.Cols{"chat_session_id": chatID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := testutil.Cols{
				"workspace_id": testWorkspaceID, "uploader_type": "member", "uploader_id": testUserID,
				"filename": "staged.txt", "url": "https://example.test/staged.txt", "content_type": "text/plain", "size_bytes": 4,
			}
			good := dbfx.Insert(t, "attachment", fields)
			for key, value := range tc.owner {
				fields[key] = value
			}
			bad := dbfx.Insert(t, "attachment", fields)
			testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, body([]string{good, bad}))).Want(409)
			if pending() != 0 || evidence() != 0 {
				t.Fatal("rejected evidence left a publication or comment")
			}
			if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id=$1 AND issue_id IS NULL AND comment_id IS NULL`, good) != 1 {
				t.Fatal("failed publication consumed an otherwise valid staged upload")
			}
			if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id=$1 AND issue_id=$2`, bad, issue.ID) != 0 {
				t.Fatal("failed publication reassigned an unavailable upload")
			}
			var current int64
			dbfx.QueryRow(t, `SELECT revision FROM issue WHERE id=$1`, issue.ID).Scan(&current)
			if current != issue.Revision {
				t.Fatalf("failed publication bumped revision to %d from %d", current, issue.Revision)
			}
		})
	}
}

func TestYixiezuoPublishRejectsConcurrentUnreviewedRevision(t *testing.T) {
	_, issue := prepareManualImport(t)
	var digest string
	dbfx.QueryRow(t, `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE issue_id=$1`, issue.ID).Scan(&digest)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, `SELECT id FROM issue WHERE id=$1 FOR UPDATE`, issue.ID); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := manualIssueRequest("POST", issue.ID, map[string]any{
		"summary": "Reviewed original revision", "revision": issue.Revision,
		"source_digest": digest, "confirmed": true,
	})
	done := make(chan error, 1)
	go func() { testHandler.PublishYixiezuoResult(rr, req); done <- nil }()
	waitForBlockedQuery(t, done, "CreateComment", "LockIssueForDescriptionUpdate")
	if _, err := holder.Exec(ctx, `UPDATE issue SET title='Unreviewed concurrent change', revision=revision+1 WHERE id=$1`, issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if rr.Code != http.StatusConflict {
		t.Fatalf("concurrent edit accepted: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, issue.ID) != 0 {
		t.Fatal("rejected publication left an evidence comment")
	}
	if dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID) != 0 {
		t.Fatal("rejected publication queued an external write")
	}
}

func TestYixiezuoPublishBindsStagedEvidenceAtomically(t *testing.T) {
	_, issue := prepareManualImport(t)
	var digest string
	dbfx.QueryRow(t, `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE issue_id=$1`, issue.ID).Scan(&digest)
	att, err := testHandler.Queries.CreateAttachment(t.Context(), db.CreateAttachmentParams{
		ID: dbid.NewV7(), WorkspaceID: parseUUID(testWorkspaceID),
		UploaderType: "member", UploaderID: parseUUID(testUserID),
		Filename: "evidence.txt", Url: "https://example.test/evidence.txt", ContentType: "text/plain", SizeBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM attachment WHERE id=$1`, att.ID)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id=$1`, issue.ID)
	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM issue WHERE id=$1`, issue.ID).Scan(&revision)
	if revision != issue.Revision {
		t.Fatalf("staging changed revision from %d to %d", issue.Revision, revision)
	}
	var op yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", issue.ID, map[string]any{
		"summary": "Verified", "revision": issue.Revision, "confirmed": true,
		"source_digest": digest, "attachment_ids": []string{uuidToString(att.ID)},
	})).Want(201).JSON(&op)
	if dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id=$1 AND issue_id=$2 AND comment_id IN (SELECT id FROM comment WHERE issue_id=$2)`, att.ID, issue.ID) != 1 {
		t.Fatal("staged evidence was not bound to the publication comment")
	}
	dbfx.QueryRow(t, `SELECT revision FROM issue WHERE id=$1`, issue.ID).Scan(&revision)
	if op.Payload.Revision != revision || revision != issue.Revision+1 {
		t.Fatalf("publication revision=%d issue=%d reviewed=%d", op.Payload.Revision, revision, issue.Revision)
	}
}

func TestYixiezuoRefreshPublishesUpdatedIssueAndPropertyCatalog(t *testing.T) {
	_, issue := prepareManualImport(t)
	var op yixiezuo.Operation
	testutil.Call(t, testHandler.RefreshYixiezuoImport, manualIssueRequest("POST", issue.ID, map[string]any{})).Want(201).JSON(&op)
	var claimed struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&claimed)
	var raw []byte
	dbfx.QueryRow(t, `SELECT snapshot FROM yixiezuo_import WHERE issue_id=$1`, issue.ID).Scan(&raw)
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Fields = []yixiezuo.SourceField{{Name: "Refresh field", Value: "new value"}, {Name: "截止日期", Value: "2026-10-31"}}
	snapshot.Status = "QA"
	var seen []events.Event
	bus := events.New()
	bus.SubscribeAll(func(e events.Event) { seen = append(seen, e) })
	h := *testHandler
	h.Bus = bus
	h.IssueService = service.NewIssueService(h.Queries, testPool, bus, nil, nil)
	receipt := yixiezuo.Completion{LeaseToken: claimed.Operation.LeaseToken, State: "succeeded", Snapshot: &snapshot}
	testutil.Call(t, h.CompleteYixiezuoOperation, manualOperationRequest("POST", op.ID, receipt)).Want(200)
	if len(seen) != 2 || seen[0].Type != protocol.EventIssueUpdated || seen[1].Type != protocol.EventIssuePropertiesChanged {
		t.Fatalf("expected owner snapshot then property/catalog invalidation, got %+v", seen)
	}
	owner := seen[0].Payload.(map[string]any)["issue"].(IssueResponse)
	if owner.DueDate == nil || *owner.DueDate != "2026-10-31" || owner.Metadata["yixiezuo_status"] != "QA" {
		t.Fatalf("stale owner event: %+v", owner)
	}
	properties := seen[1].Payload.(map[string]any)
	if properties["issue_revision"] != owner.Revision {
		t.Fatalf("event revisions differ: %v vs %d", properties["issue_revision"], owner.Revision)
	}
	testutil.Call(t, h.CompleteYixiezuoOperation, manualOperationRequest("POST", op.ID, receipt)).Want(200)
	if len(seen) != 2 {
		t.Fatal("receipt replay duplicated change events")
	}
}
