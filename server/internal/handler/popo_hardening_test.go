package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func hardeningCommand(t *testing.T, e popoP2Env, kind string, payload map[string]any) db.PopoBridgeCommand {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	row, err := testHandler.Queries.EnqueuePopoBridgeCommand(context.Background(), db.EnqueuePopoBridgeCommandParams{
		WorkspaceID: parseUUID(testWorkspaceID), BridgeID: parseUUID(e.bridgeID), InstallationID: parseUUID(e.install),
		Type: kind, DeliveryID: dbid.NewV7(), Payload: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestPopoRevokeFencesPendingLeasedAndPreflight(t *testing.T) {
	e := popoP2Setup(t)
	first := hardeningCommand(t, e, "send", map[string]any{"robot_id": e.robotID, "chat_id": e.sender, "text": "queued"})
	_, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(e.bridgeID), 0)
	if err != nil {
		t.Fatal(err)
	}
	second := hardeningCommand(t, e, "send", map[string]any{"robot_id": e.robotID, "chat_id": e.sender, "text": "pending"})
	if err := testHandler.PopoInstall.Revoke(context.Background(), parseUUID(e.install)); err != nil {
		t.Fatal(err)
	}
	for _, command := range []db.PopoBridgeCommand{first, second} {
		row, err := testHandler.Queries.GetPopoBridgeCommandForBridge(context.Background(), db.GetPopoBridgeCommandForBridgeParams{ID: command.ID, BridgeID: parseUUID(e.bridgeID)})
		if err != nil || row.Status != "cancelled" {
			t.Fatalf("command=%+v error=%v", row, err)
		}
		req := popoBearer(withURLParam(testutil.JSONRequest(http.MethodPost, "/authorize", nil), "id", util.UUIDToString(command.ID)), e.token)
		var response struct {
			Allowed bool `json:"allowed"`
		}
		testutil.Call(t, testHandler.AuthorizePopoBridgeCommand, req).Want(http.StatusOK).JSON(&response)
		if response.Allowed {
			t.Fatal("revoked installation authorized a send")
		}
	}
	rows, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(e.bridgeID), 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("revoke left leaseable commands: %v %v", rows, err)
	}
}

func TestPopoReceiptAndQuoteCommitTogetherAndReplayRepairs(t *testing.T) {
	e := popoP2Setup(t)
	e.inbound(t, "receipt-source", "/issue receipt source", nil)
	var binding string
	if err := testPool.QueryRow(context.Background(), "SELECT id FROM channel_chat_session_binding WHERE installation_id=$1", e.install).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	command := hardeningCommand(t, e, "send", map[string]any{"robot_id": e.robotID, "chat_id": e.sender, "text": "answer", "binding_id": binding, "route_revision": 1})
	lock, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(context.Background(), "LOCK TABLE channel_outbound_message IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	receipt := popo.CommandReceipt{Status: "delivered", RemoteMessageID: "receipt-atomic-" + e.bridgeID}
	if _, err := testHandler.PopoBridge.RecordReceipt(ctx, command.ID, parseUUID(e.bridgeID), receipt); err == nil {
		t.Fatal("blocked quote write must fail")
	}
	if err := lock.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err := testHandler.Queries.GetPopoBridgeCommandForBridge(context.Background(), db.GetPopoBridgeCommandForBridgeParams{ID: command.ID, BridgeID: parseUUID(e.bridgeID)})
	if err != nil || row.Status != "pending" {
		t.Fatalf("receipt escaped rollback: status=%s error=%v", row.Status, err)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), command.ID, parseUUID(e.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "DELETE FROM channel_outbound_message WHERE installation_id=$1 AND channel_message_id=$2", e.install, receipt.RemoteMessageID)
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), command.ID, parseUUID(e.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM channel_outbound_message WHERE installation_id=$1 AND channel_message_id=$2", e.install, receipt.RemoteMessageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("mapping not repaired: count=%d error=%v", count, err)
	}
}

func TestPopoRegistrationLeaseAndReceiptAreNotMessageDelivery(t *testing.T) {
	e := popoP2Setup(t)
	command := hardeningCommand(t, e, "register_qr", map[string]any{"registration_id": "test"})
	rows, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(e.bridgeID), 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("lease=%v error=%v", rows, err)
	}
	if time.Until(rows[0].LeaseExpiresAt.Time) < popo.RegistrationTTL {
		t.Fatal("registration lease expires while scanner is still waiting")
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), command.ID, parseUUID(e.bridgeID), popo.CommandReceipt{Status: "delivered"}); err != nil {
		t.Fatalf("non-message completion required a remote message id: %v", err)
	}
}

func TestPopoRecoveryFindsCommittedCommentAndStatusWithoutEvents(t *testing.T) {
	e := popoP2Setup(t)
	e.inbound(t, "recover-source", "/issue recovery source", nil)
	var issueID string
	if err := testPool.QueryRow(context.Background(), "SELECT issue_id FROM channel_issue_source WHERE installation_id=$1", e.install).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	comment := dbfx.Insert(t, "comment", testutil.Cols{"issue_id": issueID, "workspace_id": testWorkspaceID, "author_type": "member", "author_id": testUserID, "content": "committed without publication", "type": "comment"})
	out := popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default())
	for n := 0; n < 2; n++ {
		if _, err := out.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'comment_id'=$2", e.install, comment).Scan(&count); err != nil || count != 1 {
		t.Fatalf("comment recovery count=%d error=%v", count, err)
	}
	dbfx.Exec(t, "UPDATE popo_bridge_command SET status='unknown' WHERE installation_id=$1 AND payload->>'comment_id'=$2", e.install, comment)
	dbfx.Exec(t, "UPDATE issue SET status='blocked', revision=revision+1 WHERE id=$1", issueID)
	if _, err := out.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "UPDATE issue SET title=title || ' renamed', revision=revision+1 WHERE id=$1", issueID)
	if _, err := out.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'outbound_kind'='issue_status'", e.install).Scan(&count); err != nil || count != 1 {
		t.Fatalf("status replay count=%d error=%v", count, err)
	}
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'comment_id'=$2 AND status='unknown'", e.install, comment).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unknown delivery was retried: count=%d error=%v", count, err)
	}
}

func TestPopoGoHandlerPythonDeliveryContract(t *testing.T) {
	python, root := os.Getenv("MULTICA_POPO_TEST_PYTHON"), os.Getenv("MULTICA_POPO_TEST_DJ01BOT_ROOT")
	if python == "" || root == "" {
		t.Skip("set explicit Python and DJ01Bot checkout for the fake-POPO cross-repository contract")
	}
	e := popoP2Setup(t)
	command := hardeningCommand(t, e, "send", map[string]any{
		"robot_id": e.robotID, "chat_id": "contract-group", "chat_type": "group", "text": "Task complete", "reply_to_message_id": "source-message",
		"attachments": []map[string]string{{"filename": "reply.txt", "mime_type": "text/plain", "download_path": "/api/popo/bridge/media/outbound/test"}},
	})
	req := popoBearer(testutil.JSONRequest(http.MethodGet, "/commands?wait_ms=0", nil), e.token)
	var envelope map[string]any
	testutil.Call(t, testHandler.ListPopoBridgeCommands, req).Want(http.StatusOK).JSON(&envelope)
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command(python, filepath.Join(root, "tests", "multica_wire_probe.py"))
	process.Stdin = bytes.NewReader(wire)
	output, err := process.CombinedOutput()
	if err != nil {
		t.Fatalf("Python wire probe: %v\n%s", err, output)
	}
	var result struct {
		Sent []struct {
			ChatID      string   `json:"chat_id"`
			Text        string   `json:"text"`
			ReplyTo     string   `json:"reply_to"`
			Attachments []string `json:"attachments"`
		} `json:"sent"`
		Receipts []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			RemoteID string `json:"remote_message_id"`
		} `json:"receipts"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode probe: %v\n%s", err, output)
	}
	if len(result.Sent) != 1 || result.Sent[0].ChatID != "contract-group" || result.Sent[0].ReplyTo != "source-message" || len(result.Sent[0].Attachments) != 1 {
		t.Fatalf("wire lost delivery fields: %+v", result)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != "delivered" {
		t.Fatalf("receipt=%+v", result.Receipts)
	}
	row, err := testHandler.PopoBridge.RecordReceipt(context.Background(), command.ID, parseUUID(e.bridgeID), popo.CommandReceipt{Status: result.Receipts[0].Status, RemoteMessageID: result.Receipts[0].RemoteID})
	if err != nil || row.Status != "delivered" {
		t.Fatalf("Python receipt rejected by Go: %v %+v", err, row)
	}
}

func TestPopoRecoveryFindsChatResultWithoutPublishingOrRerunning(t *testing.T) {
	e := popoP2Setup(t)
	e.inbound(t, "chat-recovery", "prepare a result", nil)
	var taskID, sessionID string
	if err := testPool.QueryRow(context.Background(), `SELECT t.id, t.chat_session_id
		FROM agent_task_queue t JOIN channel_task_delivery d ON d.task_id=t.id
		WHERE d.installation_id=$1`, e.install).Scan(&taskID, &sessionID); err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": sessionID, "task_id": taskID, "role": "assistant", "content": "durable final result"})
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1", taskID)
	before := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", e.agentID)
	out := popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default())
	for n := 0; n < 2; n++ {
		if _, err := out.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if count := dbfx.Count(t, "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'task_id'=$2 AND payload->>'text'='durable final result'", e.install, taskID); count != 1 {
		t.Fatalf("recovered chat replies=%d", count)
	}
	if after := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", e.agentID); after != before {
		t.Fatalf("recovery created a new task: %d -> %d", before, after)
	}
}

func TestPopoRecoverySkipsDeletedCommentsAndKeepsAttachmentOnlyComments(t *testing.T) {
	e := popoP2Setup(t)
	e.inbound(t, "comment-recovery", "/issue comment recovery", nil)
	var issueID string
	if err := testPool.QueryRow(context.Background(), "SELECT issue_id FROM channel_issue_source WHERE installation_id=$1", e.install).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	deleted := dbfx.Insert(t, "comment", testutil.Cols{"issue_id": issueID, "workspace_id": testWorkspaceID, "author_type": "member", "author_id": testUserID, "content": "deleted", "type": "comment", "deleted_at": time.Now()})
	mediaOnly := dbfx.Insert(t, "comment", testutil.Cols{"issue_id": issueID, "workspace_id": testWorkspaceID, "author_type": "member", "author_id": testUserID, "content": "", "type": "comment"})
	dbfx.Insert(t, "attachment", testutil.Cols{"comment_id": mediaOnly, "workspace_id": testWorkspaceID,
		"uploader_type": "member", "uploader_id": testUserID, "filename": "result.txt", "url": "https://example.invalid/result.txt", "content_type": "text/plain", "size_bytes": 1})
	out := popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default())
	if _, err := out.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count := dbfx.Count(t, "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'comment_id'=$2", e.install, deleted); count != 0 {
		t.Fatal("recovery sent a deleted comment")
	}
	if count := dbfx.Count(t, "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'comment_id'=$2 AND jsonb_array_length(payload->'attachments')=1", e.install, mediaOnly); count != 1 {
		t.Fatalf("attachment-only recovery count=%d", count)
	}
}

func TestPopoConcurrentSourceEnqueueKeepsOneCommandAndMediaGrant(t *testing.T) {
	e := popoP2Setup(t)
	attachmentID := util.UUIDToString(dbid.NewV7())
	item := popo.OutboundItem{InstallationID: parseUUID(e.install), ChatID: e.sender, Content: "one durable reply", SourceKey: "test:concurrent",
		Attachments: []popo.SendAttachment{{AttachmentID: attachmentID, Filename: "result.txt", DownloadPath: "/api/popo/bridge/media/outbound/" + attachmentID}}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- testHandler.PopoBridge.Enqueue(context.Background(), item)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count := dbfx.Count(t, "SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1", e.install); count != 1 {
		t.Fatalf("concurrent commands=%d", count)
	}
	if count := dbfx.Count(t, "SELECT count(*) FROM popo_outbound_media_grant WHERE installation_id=$1", e.install); count != 1 {
		t.Fatalf("concurrent grants=%d", count)
	}
}
