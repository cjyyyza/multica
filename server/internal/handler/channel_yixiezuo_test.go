package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func prepareChannelSource(t *testing.T, e popoP2Env, externalID string) (string, yixiezuo.Snapshot) {
	t.Helper()
	e.inbound(t, "source-preview", "/yixiezuo preview https://dj01.pm.netease.com/issues/"+externalID, nil)
	var id string
	dbfx.QueryRow(t, `SELECT id FROM yixiezuo_operation WHERE workspace_id=$1 AND kind='preview' AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, e.install).Scan(&id)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM yixiezuo_operation WHERE workspace_id=$1 AND payload->'source'->>'id'=$2`, testWorkspaceID, externalID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM yixiezuo_import WHERE workspace_id=$1 AND external_id=$2`, testWorkspaceID, externalID)
	})
	source, err := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/" + externalID)
	if err != nil {
		t.Fatal(err)
	}
	lock := int64(4)
	snapshot := yixiezuo.Snapshot{Source: source, Title: "UE source " + externalID, Description: "Fixture reproduction steps", Status: "Ready", LockVersion: &lock, Attachments: []yixiezuo.Attachment{}, Comments: []yixiezuo.Comment{}, Fields: []yixiezuo.SourceField{}, Statuses: []yixiezuo.Status{{ID: 7, Name: "QA"}}, Warnings: []string{}}
	completeChannelSource(t, id, "succeeded", &snapshot)
	return id, snapshot
}

func completeChannelSource(t *testing.T, id, state string, snapshot *yixiezuo.Snapshot) {
	t.Helper()
	var claim struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&claim)
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", id, yixiezuo.Completion{LeaseToken: claim.Operation.LeaseToken, State: state, Snapshot: snapshot})).Want(200)
}

func importedChannelIssue(t *testing.T, externalID string) db.Issue {
	t.Helper()
	var id string
	dbfx.QueryRow(t, `SELECT issue_id FROM yixiezuo_import WHERE workspace_id=$1 AND external_id=$2`, testWorkspaceID, externalID).Scan(&id)
	issue, err := testHandler.Queries.GetIssueInWorkspace(context.Background(), db.GetIssueInWorkspaceParams{WorkspaceID: parseUUID(testWorkspaceID), ID: parseUUID(id)})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func sourceReviewID(t *testing.T, issue db.Issue) string {
	t.Helper()
	var id string
	dbfx.QueryRow(t, `SELECT id FROM yixiezuo_operation WHERE issue_id=$1 AND kind='review' ORDER BY created_at DESC LIMIT 1`, issue.ID).Scan(&id)
	return id
}

func TestPopoYixiezuoImportKeepsP4ProjectAndNativeTaskControls(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	projectID := dbfx.Project(t, "UE integration project")
	dbfx.Insert(t, "project_resource", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": projectID, "resource_type": "perforce_depot", "resource_ref": `{"port":"p4.test:1666","depot":"//depot/UE","stream":"//depot/UE/main","changelist":"1234"}`, "position": 0})
	preview, _ := prepareChannelSource(t, e, "880101")
	e.inbound(t, "source-preview", "/yixiezuo preview https://dj01.pm.netease.com/issues/880101", nil)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE kind='preview' AND payload->'channel'->>'installation_id'=$1`, e.install); got != 1 {
		t.Fatalf("preview replay created %d operations", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, e.agentID); got != 0 {
		t.Fatalf("preview started %d runs", got)
	}
	e.inbound(t, "source-show", "/yixiezuo show "+preview, nil)
	e.inbound(t, "source-unconfirmed", "/yixiezuo import "+preview, nil)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE external_id='880101' AND workspace_id=$1`, testWorkspaceID); got != 0 {
		t.Fatal("import did not require confirmation")
	}
	e.inbound(t, "source-import", "/yixiezuo import "+preview+" --confirm --project "+projectID, nil)
	e.inbound(t, "source-import", "/yixiezuo import "+preview+" --confirm --project "+projectID, nil)
	issue := importedChannelIssue(t, "880101")
	if issue.AssigneeID.Valid || uuidToString(issue.ProjectID) != projectID {
		t.Fatalf("import changed assignment/project: %+v", issue)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE external_id='880101' AND workspace_id=$1`, testWorkspaceID); got != 1 {
		t.Fatalf("import count=%d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, issue.ID); got != 0 {
		t.Fatal("import started execution")
	}
	source, err := testHandler.Queries.GetChannelIssueSourceByIssue(context.Background(), issue.ID)
	if err != nil || uuidToString(source.InstallationID) != e.install || source.ChannelChatID != e.sender {
		t.Fatalf("notification route=%+v err=%v", source, err)
	}
	ident := fmt.Sprintf("HAN-%d", issue.Number)
	e.inbound(t, "source-comment", "/reply "+ident+" Please use the selected UE project.", nil)
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content='Please use the selected UE project.'`, issue.ID); got != 1 {
		t.Fatal("POPO reply did not reach the imported task")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, e.agentID); got != 0 {
		t.Fatal("comment on unassigned import started a run")
	}
	_ = e.commands(t)
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest("POST", "/api/issues/"+uuidToString(issue.ID)+"/comments", map[string]any{"content": "Native task update for POPO."}), "id", uuidToString(issue.ID))).Want(http.StatusCreated)
	if replies := strings.Join(e.commands(t), "\n"); !strings.Contains(replies, "Native task update for POPO.") {
		t.Fatalf("native task comment did not return to the originating chat: %s", replies)
	}
	// Assignment is a separate, explicit user action through the native API.
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, testRuntimeID, e.agentID)
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+uuidToString(issue.ID), map[string]any{"assignee_type": "agent", "assignee_id": e.agentID}), "id", uuidToString(issue.ID))).Want(200)
	var claim struct {
		Task *struct {
			ID       string        `json:"id"`
			IssueID  string        `json:"issue_id"`
			P4Depots []P4DepotData `json:"p4_depots"`
		} `json:"task"`
	}
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+testRuntimeID+"/claim", nil, testWorkspaceID, "ue-integration")
	testutil.Call(t, testHandler.ClaimTaskByRuntime, withURLParam(req, "runtimeId", testRuntimeID)).Want(200).JSON(&claim)
	if claim.Task == nil || claim.Task.IssueID != uuidToString(issue.ID) || len(claim.Task.P4Depots) != 1 || claim.Task.P4Depots[0].Depot != "//depot/UE" || claim.Task.P4Depots[0].Changelist != "1234" {
		t.Fatalf("P4 project did not reach claimed run: %+v", claim.Task)
	}
	e.inbound(t, "source-status", "/status "+ident, nil)
	e.inbound(t, "source-stop", "/stop "+ident, nil)
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, claim.Task.ID).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("POPO stop did not cancel imported issue's run: %s", status)
	}
}

func TestPopoYixiezuoReviewConfirmationAndReceiptAreIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	preview, snapshot := prepareChannelSource(t, e, "880102")
	e.inbound(t, "import", "/yixiezuo import "+preview+" --confirm", nil)
	issue := importedChannelIssue(t, "880102")
	ident := fmt.Sprintf("HAN-%d", issue.Number)
	e.inbound(t, "prepare-review", "/yixiezuo publish "+ident+" --status QA\nFixture result and validation evidence.", nil)
	review := sourceReviewID(t, issue)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID); got != 0 {
		t.Fatal("preparing review queued an external write")
	}
	var noWork struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(200).JSON(&noWork)
	if noWork.Operation != nil {
		t.Fatal("bridge claimed an unconfirmed review")
	}
	e.inbound(t, "confirm-review", "/yixiezuo confirm "+review, nil)
	e.inbound(t, "confirm-again", "/yixiezuo confirm "+review, nil)
	var publishID string
	var payloadJSON []byte
	dbfx.QueryRow(t, `SELECT id,payload FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID).Scan(&publishID, &payloadJSON)
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.StatusName != "QA" || payload.Summary != "Fixture result and validation evidence." || payload.Revision != issue.Revision {
		t.Fatalf("review changed during confirmation: %+v", payload)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID); got != 1 {
		t.Fatalf("confirmation queued %d writes", got)
	}
	snapshot.Status = "QA"
	completeChannelSource(t, publishID, "succeeded", &snapshot)
	e.inbound(t, "confirmed-after-completion", "/yixiezuo confirm "+review, nil)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID); got != 1 {
		t.Fatal("completed publication was queued again")
	}
	var state struct {
		Import struct {
			State string `json:"state"`
		} `json:"import"`
	}
	testutil.Call(t, testHandler.GetYixiezuoImport, manualIssueRequest("GET", uuidToString(issue.ID), nil)).Want(200).JSON(&state)
	if state.Import.State != "published" {
		t.Fatalf("publication state=%s", state.Import.State)
	}
	_ = e.commands(t)
	e.inbound(t, "show-confirmed-review", "/yixiezuo show "+review, nil)
	if replies := strings.Join(e.commands(t), "\n"); !strings.Contains(replies, "already confirmed") || !strings.Contains(replies, "succeeded") {
		t.Fatalf("confirmed review lost its actual publication state: %s", replies)
	}
}

func TestPopoYixiezuoConfirmationRejectsStaleOrForeignReviews(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for i, change := range []string{"task", "source", "expired", "chat"} {
		t.Run(change, func(t *testing.T) {
			e := popoP2Setup(t)
			preview, snapshot := prepareChannelSource(t, e, fmt.Sprint(880110+i))
			e.inbound(t, "import", "/yixiezuo import "+preview+" --confirm", nil)
			issue := importedChannelIssue(t, snapshot.Source.ID)
			ident := fmt.Sprintf("HAN-%d", issue.Number)
			e.inbound(t, "review", "/yixiezuo publish "+ident+"\nConfirmed test evidence.", nil)
			review := sourceReviewID(t, issue)
			switch change {
			case "task":
				testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+uuidToString(issue.ID), map[string]any{"title": "New local change"}), "id", uuidToString(issue.ID))).Want(200)
			case "source":
				snapshot.Description = "New source change"
				snapshot.Digest = snapshot.Fingerprint()
				raw, _ := json.Marshal(snapshot)
				dbfx.Exec(t, `UPDATE yixiezuo_import SET snapshot=$1 WHERE issue_id=$2`, raw, issue.ID)
			case "expired":
				dbfx.Exec(t, `UPDATE yixiezuo_operation SET created_at=now()-interval '16 minutes' WHERE id=$1`, review)
			}
			if change == "chat" {
				e.inboundChat(t, "confirm", "/yixiezuo confirm "+review, "other-chat", "group", true, nil)
			} else {
				e.inbound(t, "confirm", "/yixiezuo confirm "+review, nil)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id=$1 AND kind='publish'`, issue.ID); got != 0 {
				t.Fatalf("%s review queued %d writes", change, got)
			}
			if change == "task" {
				row, _ := testHandler.Queries.GetIssueInWorkspace(context.Background(), db.GetIssueInWorkspaceParams{WorkspaceID: parseUUID(testWorkspaceID), ID: issue.ID})
				if row.Title != "New local change" {
					t.Fatal("source workflow overwrote local edits")
				}
			}
		})
	}
}

func TestPopoYixiezuoSourceOperationScopesActorAndWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	preview, _ := prepareChannelSource(t, e, "880120")
	scope := &yixiezuo.ChannelScope{InstallationID: e.install, ChatID: e.sender, ChatType: "p2p"}
	for _, ids := range [][2]pgtype.UUID{
		{parseUUID(testWorkspaceID), parseUUID("22222222-2222-2222-2222-222222222222")},
		{parseUUID("33333333-3333-3333-3333-333333333333"), parseUUID(testUserID)},
	} {
		_, _, err := testHandler.sourceChatOperation(context.Background(), ids[0], ids[1], preview, scope)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("foreign operation was accessible: %v", err)
		}
	}
}

func TestPopoYixiezuoCanAssociateExistingWebImportWithoutOverwritingIt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	_, original := prepareManualImport(t)
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+original.ID, map[string]any{"title": "Preserve this local edit"}), "id", original.ID)).Want(200)
	e := popoP2Setup(t)
	preview, _ := prepareChannelSource(t, e, "12")
	e.inbound(t, "associate-existing", "/yixiezuo import "+preview+" --confirm", nil)
	issue := importedChannelIssue(t, "12")
	if uuidToString(issue.ID) != original.ID || issue.Title != "Preserve this local edit" {
		t.Fatal("repeat import replaced the original task")
	}
	source, err := testHandler.Queries.GetChannelIssueSourceByIssue(context.Background(), issue.ID)
	if err != nil || uuidToString(source.InstallationID) != e.install || source.ChannelChatID != e.sender {
		t.Fatalf("web import did not acquire its explicit chat route: %+v %v", source, err)
	}
}

func TestPopoYixiezuoRefreshPreservesNativeTaskFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	preview, snapshot := prepareChannelSource(t, e, "880121")
	e.inbound(t, "import", "/yixiezuo import "+preview+" --confirm", nil)
	issue := importedChannelIssue(t, "880121")
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+uuidToString(issue.ID), map[string]any{"title": "Local task title", "description": "Local task investigation"}), "id", uuidToString(issue.ID))).Want(200)
	before := importedChannelIssue(t, "880121")
	e.inbound(t, "refresh", fmt.Sprintf("/yixiezuo refresh HAN-%d", issue.Number), nil)
	var operationID string
	dbfx.QueryRow(t, `SELECT id FROM yixiezuo_operation WHERE issue_id=$1 AND kind='refresh'`, issue.ID).Scan(&operationID)
	snapshot.Title, snapshot.Description, snapshot.Status = "Changed source title", "Changed source description", "QA"
	completeChannelSource(t, operationID, "succeeded", &snapshot)
	after := importedChannelIssue(t, "880121")
	if after.Title != before.Title || after.Description != before.Description || after.Status != before.Status || after.AssigneeID != before.AssigneeID || after.Revision != before.Revision {
		t.Fatal("source refresh overwrote native task fields")
	}
	var sourceStatus string
	dbfx.QueryRow(t, `SELECT snapshot->>'status' FROM yixiezuo_import WHERE issue_id=$1`, issue.ID).Scan(&sourceStatus)
	if sourceStatus != "QA" {
		t.Fatalf("source snapshot did not refresh: %s", sourceStatus)
	}
}
