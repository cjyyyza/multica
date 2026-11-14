package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Every changelist on the shared depot must reference a 易协作 ticket. The
// ticket comes from a structured issue field when one exists; an imported
// issue IS a ticket, so its import's external_id wins. Text markers remain the
// manual fallback.
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

	// Other handler tests import 易协作 issues into this workspace, and the
	// import creates the source-link property definition. Reuse it instead of
	// inserting a duplicate, which the case-insensitive name index rejects.
	ensureSourceLinkProperty := func(t *testing.T) string {
		t.Helper()
		var id string
		err := testPool.QueryRow(ctx, `
			SELECT id::text FROM issue_property
			WHERE workspace_id = $1 AND lower(name) = lower($2)`,
			testWorkspaceID, yixiezuo.SourceLinkPropertyName).Scan(&id)
		switch {
		case err == nil:
			return id
		case !errors.Is(err, pgx.ErrNoRows):
			t.Fatalf("load source link property: %v", err)
		}
		return dbfx.Insert(t, "issue_property", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"name":         yixiezuo.SourceLinkPropertyName,
			"type":         "multi_url",
		})
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

	t.Run("import outranks the structured metadata", func(t *testing.T) {
		issueID := dbfx.Issue(t, "imported issue", testutil.Cols{
			"description": "no marker here",
			"metadata":    testutil.Raw(`'{"yixiezuo_id":"660011"}'::jsonb`),
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

	t.Run("易协作 metadata fills the ticket when there is no import", func(t *testing.T) {
		// The issue UI renders this metadata as the 易协作 source, so a task
		// linked without importing it must not look ticket-less to the daemon.
		issueID := dbfx.Issue(t, "Fix the crash", testutil.Cols{
			"description": "no marker here",
			"metadata":    testutil.Raw(`'{"yixiezuo_id":"660011"}'::jsonb`),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "660011" {
			t.Fatalf("ticket = %q, want 660011 from metadata", got)
		}
	})

	t.Run("numeric metadata is read as a ticket", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the crash", testutil.Cols{
			"metadata": testutil.Raw(`'{"yixiezuo_id":660012}'::jsonb`),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "660012" {
			t.Fatalf("ticket = %q, want 660012 from numeric metadata", got)
		}
	})

	t.Run("non-numeric metadata falls through to the text", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the crash", testutil.Cols{
			"description": "see refs #773344 for the ticket",
			"metadata":    testutil.Raw(`'{"yixiezuo_id":"DJ-7"}'::jsonb`),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "773344" {
			t.Fatalf("ticket = %q, want 773344 from the description", got)
		}
	})

	t.Run("metadata outranks a description marker", func(t *testing.T) {
		issueID := dbfx.Issue(t, "Fix the crash", testutil.Cols{
			"description": "stale refs #990044",
			"metadata":    testutil.Raw(`'{"yixiezuo_id":"660013"}'::jsonb`),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "660013" {
			t.Fatalf("ticket = %q, want the structured metadata 660013", got)
		}
	})

	t.Run("易协作 v6/my link property fills the ticket", func(t *testing.T) {
		propertyID := ensureSourceLinkProperty(t)
		issueID := dbfx.Issue(t, "linked through the issue field", testutil.Cols{
			"properties": testutil.Raw(fmt.Sprintf(
				`jsonb_build_object('%s', '["https://dj01.pm.netease.com/v6/my?issue_id=220033"]'::jsonb)`,
				propertyID)),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "220033" {
			t.Fatalf("ticket = %q, want 220033 from the link property", got)
		}
	})

	t.Run("易协作单子 custom property fills the ticket", func(t *testing.T) {
		propertyID := dbfx.Insert(t, "issue_property", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"name":         "易协作单子",
			"type":         "url",
		})
		issueID := dbfx.Issue(t, "linked through the custom ticket field", testutil.Cols{
			"properties": testutil.Raw(fmt.Sprintf(
				`jsonb_build_object('%s', to_jsonb('https://dj01.pm.netease.com/v6/issues/issue_109425'::text))`,
				propertyID)),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "109425" {
			t.Fatalf("ticket = %q, want 109425 from the 易协作单子 property", got)
		}
	})

	t.Run("link property pointing below the issue falls through to the text", func(t *testing.T) {
		propertyID := ensureSourceLinkProperty(t)
		issueID := dbfx.Issue(t, "linked through the issue field", testutil.Cols{
			"description": "real refs #770055",
			"properties": testutil.Raw(fmt.Sprintf(
				`jsonb_build_object('%s', '["https://dj01.pm.netease.com/issues/220033/attachments/5"]'::jsonb)`,
				propertyID)),
		})
		if got := testHandler.resolveIssueP4Ticket(ctx, loadIssue(t, issueID)); got != "770055" {
			t.Fatalf("ticket = %q, want 770055 from the description", got)
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

	runtimeID := dbfx.Runtime(t, "p4-ticket-claim", testutil.Cols{"daemon_id": "p4-ticket-claim"})
	agentID := dbfx.Agent(t, "P4 ticket claim", runtimeID)

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

	t.Run("from the issue's 易协作 metadata", func(t *testing.T) {
		projectID := dbfx.Project(t, "P4 metadata ticket project")
		dbfx.Insert(t, "project_resource", testutil.Cols{
			"project_id":    projectID,
			"workspace_id":  testWorkspaceID,
			"resource_type": "perforce_depot",
			"resource_ref":  `{"port":"` + p4TestPort + `","depot":"//depot/UE"}`,
			"position":      0,
		})
		issueID := dbfx.Issue(t, "P4 work without a text marker", testutil.Cols{
			"project_id": projectID,
			"metadata":   testutil.Raw(`'{"yixiezuo_id":"551144"}'::jsonb`),
		})
		dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
		if got := claim(t, "metadata claim"); got != "551144" {
			t.Fatalf("p4_ticket = %q, want 551144 from the issue metadata", got)
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
