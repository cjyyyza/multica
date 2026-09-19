package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"sync"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type popoP2Env struct {
	bridgeID string
	token    string
	robotID  string
	install  string
	sender   string
	agentID  string
}

func wirePopoEngine(t *testing.T) {
	t.Helper()
	wirePopo(t)
	if testHandler.PopoBridge == nil {
		t.Fatal("popo bridge not wired")
	}
	replier := popo.NewOutboundReplier(popo.OutboundReplierConfig{
		Binding: testHandler.PopoBindingTokens,
		Queue:   testHandler.PopoBridge,
		AppURL:  "http://localhost:3000",
		Logger:  slog.Default(),
	})
	router := engine.NewRouter(testHandler.IssueService, testHandler.TaskService, testHandler.Queries, engine.RouterConfig{
		Follow: testHandler, Logger: slog.Default(),
	})
	router.Register(popo.TypePopo, popo.NewPopoResolverSet(testHandler.Queries, testPool, replier))
	prev := testHandler.ChannelRouter
	testHandler.ChannelRouter = router
	popoOutboundOnce.Do(func() {
		popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default()).Register(testHandler.Bus)
	})
	t.Cleanup(func() {
		testHandler.ChannelRouter = prev
	})
}

var popoOutboundOnce sync.Once

func popoP2Setup(t *testing.T) popoP2Env {
	t.Helper()
	wirePopoEngine(t)
	bridgeID, token := popoRegisterBridge(t, "WIN-P2")
	robotID := "p2-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))
	agentID := createHandlerTestAgent(t, "popo-p2-agent-"+bridgeID[:8], []byte(`{}`))
	installReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentID, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	var installRow struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBot, installReq).Want(http.StatusOK).JSON(&installRow)

	sender := "alice-p2-" + bridgeID[:8] + "@corp.netease.com"
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"multica_user_id": testUserID,
		"installation_id": installRow.ID,
		"channel_type":    "popo",
		"channel_user_id": sender,
	})
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_inbound_write WHERE installation_id = $1`, installRow.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_issue_source WHERE installation_id = $1`, installRow.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_outbound_message WHERE installation_id = $1`, installRow.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1`, installRow.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_chat_session_binding WHERE installation_id = $1`, installRow.ID)
	})
	return popoP2Env{
		bridgeID: bridgeID, token: token, robotID: robotID,
		install: installRow.ID, sender: sender, agentID: agentID,
	}
}

func (e popoP2Env) inbound(t *testing.T, eventID, text string, quote map[string]string) {
	t.Helper()
	body := map[string]any{
		"protocol_version": 1,
		"event_id":         eventID,
		"robot_id":         e.robotID,
		"sender":           map[string]string{"id": e.sender, "name": "Alice"},
		"chat":             map[string]string{"id": e.sender, "type": "p2p"},
		"addressed_to_bot": true,
		"text":             text,
		"command_text":     text,
	}
	if quote != nil {
		body["quote"] = quote
	}
	accepted := testutil.Call(t, testHandler.IngestPopoBridgeInbound, popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", body), e.token)).Want(http.StatusOK).Map()
	if accepted["accepted"] != true {
		t.Fatalf("inbound %s = %+v", eventID, accepted)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if testHandler.ChannelRouter != nil {
		_ = testHandler.ChannelRouter.Drain(ctx)
	}
}

func (e popoP2Env) commands(t *testing.T) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	listReq := popoBearer(req, e.token)
	var listed struct {
		Commands []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, listReq).Want(http.StatusOK).JSON(&listed)
	out := make([]string, 0, len(listed.Commands))
	for _, cmd := range listed.Commands {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(cmd.Payload, &payload)
		out = append(out, payload.Text)
	}
	return out
}

func TestPopoIssueCommandIsIdempotent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-1", "/issue Fix login\nsteps", nil)
	e.inbound(t, "evt-issue-1", "/issue Fix login\nsteps", nil)

	n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'popo_chat' AND title = 'Fix login'`, testWorkspaceID)
	if n != 1 {
		t.Fatalf("issues = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM channel_issue_source WHERE installation_id = $1`, e.install); n != 1 {
		t.Fatalf("source rows = %d, want 1", n)
	}
}

func TestPopoQuoteCreatesCommentNotChatRun(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-q", "/issue Quote follow", nil)

	var issueID, bindingID string
	var number int32
	if err := testPool.QueryRow(context.Background(), `
		SELECT issue.id, issue.number, source.binding_id
		FROM issue
		JOIN channel_issue_source AS source ON source.issue_id = issue.id
		WHERE issue.workspace_id = $1 AND issue.title = 'Quote follow'
	`, testWorkspaceID).Scan(&issueID, &number, &bindingID); err != nil {
		t.Fatalf("load created issue: %v", err)
	}
	dbfx.InsertNoID(t, "channel_outbound_message", testutil.Cols{
		"installation_id":    e.install,
		"channel_type":       "popo",
		"channel_message_id": "quoted-run-1",
		"binding_id":         bindingID,
		"route_revision":     1,
		"outbound_kind":      "issue_created",
		"issue_id":           issueID,
	}, "installation_id = $1 AND channel_message_id = $2", e.install, "quoted-run-1")

	beforeComments := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, issueID)
	beforeChatTasks := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE chat_session_id IS NOT NULL AND agent_id = $1`, e.agentID)

	e.inbound(t, "evt-quote-1", "please continue that thread", map[string]string{"message_id": "quoted-run-1"})

	afterComments := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, issueID)
	if afterComments != beforeComments+1 {
		t.Fatalf("comments = %d -> %d, want +1", beforeComments, afterComments)
	}
	afterChatTasks := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE chat_session_id IS NOT NULL AND agent_id = $1`, e.agentID)
	if afterChatTasks != beforeChatTasks {
		t.Fatalf("chat runs = %d -> %d, quote must not start a chat run", beforeChatTasks, afterChatTasks)
	}
}

func TestPopoReplyStatusStop(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-cmd", "/issue Status loop", nil)

	var issueID string
	var number int32
	if err := testPool.QueryRow(context.Background(), `
		SELECT id, number FROM issue WHERE workspace_id = $1 AND title = 'Status loop'
	`, testWorkspaceID).Scan(&issueID, &number); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	ident := "HAN-" + strconv.Itoa(int(number))

	e.inbound(t, "evt-reply-1", "/reply "+ident+" /note keep going", nil)
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = '/note keep going'`, issueID); n != 1 {
		t.Fatalf("reply comments = %d, want 1", n)
	}

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id FROM agent_task_queue
		WHERE issue_id = $1 AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
		ORDER BY created_at DESC LIMIT 1
	`, issueID).Scan(&taskID); err != nil {
		t.Fatalf("load active issue run: %v", err)
	}
	e.inbound(t, "evt-status-1", "/status "+ident, nil)
	e.inbound(t, "evt-stop-1", "/stop "+ident, nil)

	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("task status = %s, want cancelled", status)
	}
	var issueStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("load issue status: %v", err)
	}
	if issueStatus == "cancelled" {
		t.Fatal("/stop must not auto-set the issue to cancelled")
	}

	texts := strings.Join(e.commands(t), "\n")
	if !strings.Contains(texts, "Issue status") || !strings.Contains(texts, "Current run") {
		t.Fatalf("status reply missing from %q", texts)
	}
}

func TestPopoOriginCommentIsNotRedelivered(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-echo", "/issue Echo loop", nil)
	var issueID, bindingID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT issue.id, source.binding_id
		FROM issue JOIN channel_issue_source AS source ON source.issue_id = issue.id
		WHERE issue.title = 'Echo loop'
	`).Scan(&issueID, &bindingID); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	dbfx.InsertNoID(t, "channel_outbound_message", testutil.Cols{
		"installation_id": e.install, "channel_type": "popo",
		"channel_message_id": "echo-src", "binding_id": bindingID,
		"route_revision": 1, "outbound_kind": "issue_created", "issue_id": issueID,
	}, "installation_id = $1 AND channel_message_id = $2", e.install, "echo-src")

	e.inbound(t, "evt-echo-comment", "from popo", map[string]string{"uuid": "echo-src"})
	texts := strings.Join(e.commands(t), "\n")
	if strings.Contains(texts, "New comment on this issue") {
		t.Fatalf("POPO-origin comment echoed back: %q", texts)
	}
}

func TestWebCommentOnPopoIssueEnqueuesSend(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-web", "/issue Web follow", nil)
	var issueID string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM issue WHERE title = 'Web follow'`).Scan(&issueID); err != nil {
		t.Fatalf("load issue: %v", err)
	}

	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]string{
		"content": "from the web",
	}), "id", issueID)
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated)
	time.Sleep(50 * time.Millisecond)

	texts := strings.Join(e.commands(t), "\n")
	if !strings.Contains(texts, "from the web") {
		t.Fatalf("web comment did not enqueue popo send: %q", texts)
	}
}

func TestPopoReassignmentStopsOldBot(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-issue-reassign", "/issue Reassign loop", nil)
	var issueID string
	var number int32
	if err := testPool.QueryRow(context.Background(), `
		SELECT id, number FROM issue WHERE title = 'Reassign loop'
	`).Scan(&issueID, &number); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	other := createHandlerTestAgent(t, "popo-p2-other-"+e.bridgeID[:8], []byte(`{}`))
	dbfx.Exec(t, `UPDATE issue SET assignee_type = 'agent', assignee_id = $1 WHERE id = $2`, other, issueID)

	ident := "HAN-" + strconv.Itoa(int(number))
	e.inbound(t, "evt-reply-reassign", "/reply "+ident+" still me", nil)
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = 'still me'`, issueID); n != 0 {
		t.Fatalf("old bot still commented after reassignment")
	}
	texts := strings.Join(e.commands(t), "\n")
	if !strings.Contains(texts, "now assigned") {
		t.Fatalf("old bot did not tell the new owner: %q", texts)
	}
}
