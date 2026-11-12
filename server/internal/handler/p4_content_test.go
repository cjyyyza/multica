package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func p4ContentDaemonRequest(method, path string, body any, workspaceID, daemonID string, params ...string) *http.Request {
	req := p4DaemonRequest(method, path, body, workspaceID, daemonID, params...)
	return req.WithContext(middleware.WithDaemonTokenOwnerID(req.Context(), testUserID))
}

func TestP4ContentStoreClaimsAndExpires(t *testing.T) {
	store := NewP4ContentStore(nil)
	ctx := context.Background()
	req, err := store.Create(ctx, P4ContentRequest{RuntimeID: "runtime-a"})
	if err != nil {
		t.Fatal(err)
	}
	if other, _ := store.PopPending(ctx, "runtime-b"); other != nil {
		t.Fatal("claimed another runtime's request")
	}
	var wg sync.WaitGroup
	claims := make(chan *P4ContentRequest, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, _ := store.PopPending(ctx, "runtime-a")
			if claimed != nil {
				claims <- claimed
			}
		}()
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("claimed request %d times", len(claims))
	}
	if err := store.Complete(ctx, req.ID, &protocol.P4ChangelistContent{Source: "shelf", Diff: "+new"}, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, req.ID)
	if got.Status != "completed" || got.Result.Diff != "+new" {
		t.Fatalf("result = %+v", got)
	}
	old, _ := store.Create(ctx, P4ContentRequest{RuntimeID: "runtime-a"})
	stored := store.requests[old.ID]
	stored.CreatedAt = time.Now().Add(-p4ContentTimeout - time.Second)
	store.requests[old.ID] = stored
	got, _ = store.Get(ctx, old.ID)
	if got.Status != "failed" {
		t.Fatalf("timed-out request = %+v", got)
	}
}

func TestP4ContentRequestRoundTripAndIsolation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	h := *testHandler
	h.P4ContentStore = NewP4ContentStore(nil)
	notifier := &pendingWorkRecorder{}
	h.DaemonPendingWork = notifier
	runtime := dbfx.Runtime(t, "p4 content runtime", testutil.Cols{"daemon_id": "p4-content-daemon", "status": "online", "metadata": testutil.Raw(`'{"capabilities":["p4-content-v1"]}'::jsonb`)})
	project := p4ConfiguredProject(t, "p4 content", p4TestPort)
	issue := dbfx.Issue(t, "p4 content", testutil.Cols{"project_id": project})
	agent := dbfx.Agent(t, "p4 content agent", runtime)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "running"})
	var cl P4ChangelistResponse
	number := 894000 + int(p4LeaseTestNumber.Add(1))
	testutil.Call(t, h.ReportTaskP4Changelist, p4ContentDaemonRequest(http.MethodPost, "/", reportBody(number, number, nil), testWorkspaceID, "p4-content-daemon", "taskId", task)).Want(http.StatusOK).JSON(&cl)
	var request P4ContentRequest
	testutil.Call(t, h.RequestIssueP4Content, p4UserRequest(http.MethodPost, "/", map[string]any{"file": "//depot/A.cpp"}, issue, "changelistId", cl.ID)).Want(http.StatusOK).JSON(&request)
	if request.RuntimeID != runtime {
		t.Fatalf("selected runtime %s, want %s", request.RuntimeID, runtime)
	}
	var pending P4ContentRequest
	testutil.Call(t, h.GetIssueP4Content, p4UserRequest(http.MethodGet, "/", nil, issue, "changelistId", cl.ID, "requestId", request.ID)).Want(http.StatusOK).JSON(&pending)
	if pending.Status != "pending" || notifier.count() != 2 {
		t.Fatalf("unclaimed read did not reissue its wakeup: status=%s hints=%d", pending.Status, notifier.count())
	}
	ack, _, err := h.processHeartbeat(t.Context(), runtime, true)
	if err != nil || ack.PendingP4Content == nil || ack.PendingP4Content.ID != request.ID {
		t.Fatalf("heartbeat did not dispatch content: ack=%+v, error=%v", ack, err)
	}
	var readContext struct {
		Port           string `json:"port"`
		AllowWorkspace bool   `json:"allow_workspace"`
		File           string `json:"file"`
	}
	testutil.Call(t, h.GetDaemonP4ContentContext, p4ContentDaemonRequest(http.MethodGet, "/", nil, testWorkspaceID, "p4-content-daemon", "runtimeId", runtime, "requestId", request.ID)).Want(http.StatusOK).JSON(&readContext)
	if readContext.Port != p4TestPort || !readContext.AllowWorkspace || readContext.File != "//depot/A.cpp" {
		t.Fatalf("read context = %+v", readContext)
	}
	testutil.Call(t, h.ReportP4ContentResult, p4ContentDaemonRequest(http.MethodPost, "/", map[string]any{"result": protocol.P4ChangelistContent{Source: "shelf", Diff: "+new"}}, testWorkspaceID, "p4-content-daemon", "runtimeId", runtime, "requestId", request.ID)).Want(http.StatusOK)
	var completed P4ContentRequest
	testutil.Call(t, h.GetIssueP4Content, p4UserRequest(http.MethodGet, "/", nil, issue, "changelistId", cl.ID, "requestId", request.ID)).Want(http.StatusOK).JSON(&completed)
	if completed.Status != "completed" || completed.Result.Diff != "+new" {
		t.Fatalf("completed = %+v", completed)
	}
	other := dbfx.Issue(t, "p4 content unrelated issue")
	testutil.Call(t, h.GetIssueP4Content, p4UserRequest(http.MethodGet, "/", nil, other, "changelistId", cl.ID, "requestId", request.ID)).Want(http.StatusNotFound)
	testutil.Call(t, h.RequestIssueP4Content, p4UserRequest(http.MethodPost, "/", map[string]any{}, issue, "changelistId", "not-a-uuid")).Want(http.StatusBadRequest)
	testutil.Call(t, h.GetDaemonP4ContentContext, p4DaemonRequest(http.MethodGet, "/", nil, "22222222-2222-4222-8222-222222222222", "p4-content-daemon", "runtimeId", runtime, "requestId", request.ID)).Want(http.StatusNotFound)
}

func TestRedisP4ContentStoreSharesRequests(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	a, b := NewP4ContentStore(rdb), NewP4ContentStore(rdb)
	req, err := a.Create(ctx, P4ContentRequest{RuntimeID: "rt", File: "//depot/A.cpp"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := b.PopPending(ctx, "rt")
	if err != nil || claimed == nil || claimed.ID != req.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if duplicate, _ := a.PopPending(ctx, "rt"); duplicate != nil {
		t.Fatal("claimed twice across API nodes")
	}
	if err := b.Complete(ctx, req.ID, &protocol.P4ChangelistContent{Diff: "+new"}, ""); err != nil {
		t.Fatal(err)
	}
	completed, err := a.Get(ctx, req.ID)
	if err != nil || completed.Status != "completed" || completed.Result.Diff != "+new" {
		t.Fatalf("readback = %+v, %v", completed, err)
	}
}
