package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func yixiezuoRequest(method, path string, body any) *http.Request {
	req := newRequest(method, path, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", testWorkspaceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestYixiezuoUpsertRequiresListQueryID(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	testutil.Call(t, testHandler.UpsertYixiezuoConnection, yixiezuoRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/yixiezuo", map[string]any{
		"cli_bin": "popo-cli",
	})).Want(http.StatusBadRequest)
}

func TestYixiezuoBidirectionalSync(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_card_link WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_connection WHERE workspace_id = $1`, testWorkspaceID)
	})

	var created yixiezuoConnectionEnvelope
	testutil.Call(t, testHandler.UpsertYixiezuoConnection, yixiezuoRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/yixiezuo", map[string]any{
		"cli_bin":       "pm-cli",
		"list_query_id": "9",
		"status_map": map[string]string{
			"开发中": "in_progress",
		},
	})).Want(http.StatusOK).JSON(&created)
	if created.Connection == nil || created.Connection.CLIBin != "pm-cli" {
		t.Fatalf("upsert connection: %+v", created)
	}

	var pulled yixiezuoPullResult
	testutil.Call(t, testHandler.PullYixiezuoCards, yixiezuoRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/pull", map[string]any{
		"cards": []map[string]any{{
			"external_id": "88",
			"title":       "易协作来的任务",
			"description": "desc",
			"status_name": "开发中",
			"priority":    "高",
			"updated_at":  time.Now().UTC().Format(time.RFC3339),
		}},
	})).Want(http.StatusOK).JSON(&pulled)
	if pulled.Created != 1 {
		t.Fatalf("pull created=%d body skipped=%d updated=%d", pulled.Created, pulled.Skipped, pulled.Updated)
	}

	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT issue_id::text FROM yixiezuo_card_link
		WHERE workspace_id = $1 AND external_issue_id = '88'
	`, testWorkspaceID).Scan(&issueID); err != nil {
		t.Fatalf("lookup link: %v", err)
	}
	var status, title string
	if err := testPool.QueryRow(context.Background(), `SELECT status, title FROM issue WHERE id = $1`, issueID).Scan(&status, &title); err != nil {
		t.Fatalf("lookup issue: %v", err)
	}
	if status != "in_progress" || title != "易协作来的任务" {
		t.Fatalf("imported issue status=%q title=%q", status, title)
	}

	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
		"status": "done",
		"title":  "看板改过",
	}), "id", issueID)).Want(http.StatusOK)

	var dirty bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT dirty FROM yixiezuo_card_link WHERE issue_id = $1
	`, issueID).Scan(&dirty); err != nil {
		t.Fatalf("dirty: %v", err)
	}
	if !dirty {
		t.Fatal("kanban edit should mark the 易协作 link dirty")
	}

	var exp yixiezuoExportResponse
	testutil.Call(t, testHandler.ExportYixiezuoChanges, yixiezuoRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/export", nil)).
		Want(http.StatusOK).JSON(&exp)
	if len(exp.Updates) != 1 || exp.Updates[0].ExternalIssueID != "88" || exp.Updates[0].Issue.Status != "done" {
		encoded, _ := json.Marshal(exp)
		t.Fatalf("export updates: %s", encoded)
	}

	testutil.Call(t, testHandler.AckYixiezuoPush, yixiezuoRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/push-ack", map[string]any{
		"results": []map[string]any{{
			"issue_id":            issueID,
			"external_issue_id":   "88",
			"external_updated_at": time.Now().UTC().Format(time.RFC3339),
		}},
	})).Want(http.StatusOK)
	if err := testPool.QueryRow(context.Background(), `
		SELECT dirty FROM yixiezuo_card_link WHERE issue_id = $1
	`, issueID).Scan(&dirty); err != nil {
		t.Fatalf("dirty after ack: %v", err)
	}
	if dirty {
		t.Fatal("successful push ack should clear dirty")
	}
}

func TestYixiezuoSkipDirtyNewerLocal(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_card_link WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM yixiezuo_connection WHERE workspace_id = $1`, testWorkspaceID)
	})
	testutil.Call(t, testHandler.UpsertYixiezuoConnection, yixiezuoRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/yixiezuo", map[string]any{
		"cli_bin":       "pm-cli",
		"list_query_id": "9",
	})).Want(http.StatusOK)

	var pulled yixiezuoPullResult
	testutil.Call(t, testHandler.PullYixiezuoCards, yixiezuoRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/pull", map[string]any{
		"cards": []map[string]any{{
			"external_id": "91",
			"title":       "本地更新优先",
			"status_name": "新建",
			"updated_at":  "2020-01-01T00:00:00Z",
		}},
	})).Want(http.StatusOK).JSON(&pulled)
	if pulled.Created != 1 {
		t.Fatalf("setup created=%d", pulled.Created)
	}
	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT issue_id::text FROM yixiezuo_card_link
		WHERE workspace_id = $1 AND external_issue_id = '91'
	`, testWorkspaceID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
		"title": "看板更新",
	}), "id", issueID)).Want(http.StatusOK)

	var skipped yixiezuoPullResult
	testutil.Call(t, testHandler.PullYixiezuoCards, yixiezuoRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/yixiezuo/pull", map[string]any{
		"cards": []map[string]any{{
			"external_id": "91",
			"title":       "更旧的易协作标题",
			"status_name": "新建",
			"updated_at":  "2020-01-01T00:00:00Z",
		}},
	})).Want(http.StatusOK).JSON(&skipped)
	if skipped.Skipped != 1 {
		t.Fatalf("expected skip of stale remote, got %+v", skipped)
	}
	var title string
	if err := testPool.QueryRow(context.Background(), `SELECT title FROM issue WHERE id = $1`, issueID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "看板更新" {
		t.Fatalf("stale remote overwrote local title: %q", title)
	}
}
