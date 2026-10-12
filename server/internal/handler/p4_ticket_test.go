package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Every changelist on the shared depot must reference a 易协作 ticket. An
// imported issue IS a ticket; anything else has to say so in its own text.
func TestResolveIssueP4Ticket(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	loadIssue := func(t *testing.T, issueID string) db.Issue {
		t.Helper()
		issue, err := testHandler.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          parseUUID(issueID),
			WorkspaceID: parseUUID(testWorkspaceID),
		})
		if err != nil {
			t.Fatalf("load issue: %v", err)
		}
		return issue
	}

	t.Run("易协作 import external_id wins", func(t *testing.T) {
		issueID := dbfx.Issue(t, "imported issue with refs #999999 in the title", testutil.Cols{
			"description": "refs #888888",
		})
		dbfx.Insert(t, "yixiezuo_import", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"issue_id":     issueID,
			"source_host":  "dj01.pm.netease.com",
			"external_id":  "880101",
			"source_url":   "https://dj01.pm.netease.com/issues/880101",
			"snapshot":     testutil.Raw("'{}'::jsonb"),
			"imported_by":  testUserID,
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "880101" {
			t.Fatalf("ticket = %q, want the import's external_id 880101", got)
		}
	})

	t.Run("non-numeric external_id falls through to the text", func(t *testing.T) {
		issueID := dbfx.Issue(t, "imported with a key, not a number", testutil.Cols{
			"description": "see refs #773311 for the ticket",
		})
		dbfx.Insert(t, "yixiezuo_import", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"issue_id":     issueID,
			"source_host":  "dj01.pm.netease.com",
			"external_id":  "DJ-7",
			"source_url":   "https://dj01.pm.netease.com/issues/DJ-7",
			"snapshot":     testutil.Raw("'{}'::jsonb"),
			"imported_by":  testUserID,
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "773311" {
			t.Fatalf("ticket = %q, want 773311 from the description", got)
		}
	})

	t.Run("title before description", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the lighting refs #771122", testutil.Cols{
			"description": "refs #990011",
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "771122" {
			t.Fatalf("ticket = %q, want 771122 from the title", got)
		}
	})

	t.Run("description when the title has none", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the lighting", testutil.Cols{
			"description": "Context.\n\nrefs # 662233",
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "662233" {
			t.Fatalf("ticket = %q, want 662233 from the description", got)
		}
	})

	t.Run("no ticket anywhere", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the lighting", testutil.Cols{"description": "No ticket here."})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "" {
			t.Fatalf("ticket = %q, want empty", got)
		}
	})
}

// p4_ticket travels with the depots: a claim that cannot open Perforce files
// must not look Perforce-bound, and the daemon reads a present ticket as
// "this task has one".
func TestClaimTaskSendsP4TicketOnlyWithDepots(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	var agentID, runtimeID string
	dbfx.QueryRow(t,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 LIMIT 1`,
		testWorkspaceID,
	).Scan(&agentID, &runtimeID)

	claim := func(t *testing.T, label string) string {
		t.Helper()
		req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, "p4-ticket-claim")
		req = withURLParam(req, "runtimeId", runtimeID)
		var resp struct {
			Task *struct {
				P4Ticket string        `json:"p4_ticket"`
				P4Depots []P4DepotData `json:"p4_depots"`
			} `json:"task"`
		}
		testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&resp)
		if resp.Task == nil {
			t.Fatalf("%s: expected task in response", label)
		}
		return resp.Task.P4Ticket
	}

	t.Run("with a Perforce depot", func(t *testing.T) {
		projectID := dbfx.Project(t, "P4 ticket project")
		dbfx.Insert(t, "project_resource", testutil.Cols{
			"project_id":    projectID,
			"workspace_id":  testWorkspaceID,
			"resource_type": "perforce_depot",
			"resource_ref":  `{"port":"` + p4TestPort + `","depot":"//depot/UE"}`,
			"position":      0,
		})
		issueID := dbfx.Issue(t, "P4 work refs #551122", testutil.Cols{"project_id": projectID})
		dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
		if got := claim(t, "depot claim"); got != "551122" {
			t.Fatalf("p4_ticket = %q, want 551122", got)
		}
	})

	t.Run("without a Perforce depot", func(t *testing.T) {
		projectID := dbfx.Project(t, "Git-only ticket project")
		dbfx.Insert(t, "project_resource", testutil.Cols{
			"project_id":    projectID,
			"workspace_id":  testWorkspaceID,
			"resource_type": "github_repo",
			"resource_ref":  `{"url":"https://github.com/example/no-p4"}`,
			"position":      0,
		})
		issueID := dbfx.Issue(t, "Git work refs #551133", testutil.Cols{"project_id": projectID})
		dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
		if got := claim(t, "git claim"); got != "" {
			t.Fatalf("p4_ticket = %q, want empty without depots", got)
		}
	})
}
