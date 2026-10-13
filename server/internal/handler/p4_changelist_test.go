package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const p4TestPort = "p4-test.example.com:1666"

// p4ChangelistsEnvelope is the list shape both user reads return.
type p4ChangelistsEnvelope struct {
	Changelists []P4ChangelistResponse `json:"changelists"`
}

// seedP4Task builds the running, issue-backed task the report route needs:
// requireDaemonTaskAccessWithWorkspace resolves the workspace through the
// issue, so a task with no issue 404s before the body is read.
func seedP4Task(t *testing.T, issueID, label string) string {
	t.Helper()
	agentID := dbfx.Agent(t, label+" agent", handlerTestRuntimeID(t), testutil.Cols{
		"instructions": "",
		"custom_env":   testutil.Raw("'{}'::jsonb"),
		"custom_args":  testutil.Raw("'[]'::jsonb"),
	})
	return dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"issue_id":   issueID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
}

// p4DaemonRequest builds a daemon-token request with the chi params the router
// binds in production. workspaceID is the workspace the token is scoped to, so
// a test can hand it a foreign one and assert the ownership check.
func p4DaemonRequest(method, path string, body any, workspaceID, daemonID string, params ...string) *http.Request {
	req := testutil.JSONRequest(method, path, body)
	if len(params) > 0 {
		req = testutil.WithURLParams(req, params...)
	}
	return req.WithContext(middleware.WithDaemonContext(req.Context(), workspaceID, daemonID))
}

func p4UserRequest(method, path string, body any, issueID string, params ...string) *http.Request {
	req := newRequest(method, path, body)
	kv := append([]string{"id", issueID}, params...)
	return testutil.WithURLParams(req, kv...)
}

// reportBody is the task-report payload with the fields a test does not care
// about already filled in.
func reportBody(number, original int, over map[string]any) map[string]any {
	body := map[string]any{
		"port":            p4TestPort,
		"server_key":      "p4_test_cluster",
		"charset":         "utf8",
		"number":          number,
		"original_number": original,
		"status":          "pending",
		"client":          "tester_WS_Dev",
		"p4_user":         "tester",
		"description":     fmt.Sprintf("refs #880101 fixture %d", number),
		"ticket":          "880101",
		"files_count":     2,
		"exclusive_files": []string{"//depot/UE/a.uasset"},
		"warnings":        []map[string]any{{"kind": "not_head", "files": []string{"//depot/UE/b.uasset"}}},
	}
	for k, v := range over {
		body[k] = v
	}
	return body
}

func listP4Changelists(t *testing.T, issueID string) []P4ChangelistResponse {
	t.Helper()
	var out p4ChangelistsEnvelope
	testutil.Call(t, testHandler.ListP4ChangelistsForIssue,
		p4UserRequest(http.MethodGet, "/api/issues/"+issueID+"/p4-changelists", nil, issueID),
	).Want(http.StatusOK).JSON(&out)
	return out.Changelists
}

// A task reports the same changelist after every write, so the route has to be
// an upsert on (workspace_id, server_key, original_number) rather than an
// insert — and it has to follow the renumber a submit performs instead of
// creating a second row for the new number.
func TestReportTaskP4Changelist_UpsertsIdempotentlyAndFollowsRenumber(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "p4 report fixture")
	taskID := seedP4Task(t, issueID, "p4-report")
	path := "/api/daemon/tasks/" + taskID + "/p4/changelists"

	var first P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist,
		p4DaemonRequest(http.MethodPost, path, reportBody(795095, 795095, nil), testWorkspaceID, "p4-daemon-a", "taskId", taskID),
	).Want(http.StatusOK).JSON(&first)

	if first.Number != 795095 || first.OriginalNumber != 795095 {
		t.Fatalf("numbers = (%d, %d), want (795095, 795095)", first.Number, first.OriginalNumber)
	}
	if first.LinkSource != p4ChangelistLinkTask {
		t.Errorf("link_source = %q, want %q", first.LinkSource, p4ChangelistLinkTask)
	}
	if first.TaskID == nil || *first.TaskID != taskID {
		t.Errorf("task_id = %v, want %q", first.TaskID, taskID)
	}
	if len(first.ExclusiveFiles) != 1 || first.ExclusiveFiles[0] != "//depot/UE/a.uasset" {
		t.Errorf("exclusive_files = %v", first.ExclusiveFiles)
	}
	if len(first.Warnings) != 1 || first.Warnings[0].Kind != "not_head" {
		t.Errorf("warnings = %+v", first.Warnings)
	}
	if first.SubmittedAt != nil {
		t.Errorf("submitted_at = %v on a pending changelist", *first.SubmittedAt)
	}

	// Second report of the same changelist: more files, still shelved.
	var second P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist,
		p4DaemonRequest(http.MethodPost, path,
			reportBody(795095, 795095, map[string]any{"status": "shelved", "files_count": 5}),
			testWorkspaceID, "p4-daemon-a", "taskId", taskID),
	).Want(http.StatusOK).JSON(&second)

	if second.ID != first.ID {
		t.Fatalf("second report created a new row %s (first %s)", second.ID, first.ID)
	}
	if second.Status != p4ChangelistStatusShelved || second.FilesCount != 5 {
		t.Errorf("second report = (%q, %d), want (shelved, 5)", second.Status, second.FilesCount)
	}

	// Submitting renumbers the change; identity stays on original_number.
	var renumbered P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist,
		p4DaemonRequest(http.MethodPost, path,
			reportBody(795115, 795095, map[string]any{"status": "submitted"}),
			testWorkspaceID, "p4-daemon-a", "taskId", taskID),
	).Want(http.StatusOK).JSON(&renumbered)

	if renumbered.ID != first.ID {
		t.Fatalf("renumbered report created a new row %s (first %s)", renumbered.ID, first.ID)
	}
	if renumbered.Number != 795115 || renumbered.OriginalNumber != 795095 {
		t.Errorf("renumbered = (%d, %d), want (795115, 795095)", renumbered.Number, renumbered.OriginalNumber)
	}
	if renumbered.SubmittedAt == nil {
		t.Error("submitted changelist has no submitted_at")
	}

	if got := listP4Changelists(t, issueID); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("issue carries %d changelists, want the one row", len(got))
	}
}

// The review the daemon creates for a changelist belongs to the changelist
// card. It must reach the card, and it must not also show up in the issue's
// pull-request list, where a committed review would read as "Merged".
func TestReportTaskP4Changelist_AttachesSwarmReviewAndKeepsItOffThePullRequestList(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "p4 swarm fixture")
	taskID := seedP4Task(t, issueID, "p4-swarm")
	dbfx.Cleanup(t, `DELETE FROM swarm_review WHERE workspace_id = $1 AND review_number = 4242`, testWorkspaceID)

	var cl P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist,
		p4DaemonRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/p4/changelists",
			reportBody(795200, 795200, map[string]any{
				"status": "shelved",
				"swarm_review": map[string]any{
					"number":      4242,
					"url":         "https://swarm.example.com/reviews/4242",
					"state":       "needsReview",
					"author":      "tester",
					"description": "refs #880101 fixture review",
				},
			}),
			testWorkspaceID, "p4-daemon-swarm", "taskId", taskID),
	).Want(http.StatusOK).JSON(&cl)

	if cl.SwarmReviewID == nil {
		t.Fatal("changelist has no attached swarm review")
	}
	if cl.SwarmReviewNumber == nil || *cl.SwarmReviewNumber != 4242 {
		t.Errorf("swarm_review_number = %v, want 4242", cl.SwarmReviewNumber)
	}
	if cl.SwarmReviewURL == nil || *cl.SwarmReviewURL != "https://swarm.example.com/reviews/4242" {
		t.Errorf("swarm_review_url = %v", cl.SwarmReviewURL)
	}
	if cl.SwarmReviewState == nil || *cl.SwarmReviewState != "needsReview" {
		t.Errorf("swarm_review_state = %v", cl.SwarmReviewState)
	}

	// Link the same review through the issue route the CLI uses, then assert
	// the pull-request list still leaves it out because a changelist holds it.
	testutil.Call(t, testHandler.UpsertSwarmReviewForIssue, p4UserRequest(
		http.MethodPost, "/api/issues/"+issueID+"/swarm-reviews", map[string]any{
			"review_id":  4242,
			"swarm_url":  "https://swarm.example.com",
			"changelist": "795200",
			"state":      "needsReview",
		}, issueID,
	)).Want(http.StatusOK)

	var prs struct {
		PullRequests []GitHubPullRequestResponse `json:"pull_requests"`
	}
	testutil.Call(t, testHandler.ListPullRequestsForIssue,
		p4UserRequest(http.MethodGet, "/api/issues/"+issueID+"/pull-requests", nil, issueID),
	).Want(http.StatusOK).JSON(&prs)
	for _, pr := range prs.PullRequests {
		if pr.ID == *cl.SwarmReviewID {
			t.Fatalf("swarm review %s is on a changelist and still in the pull-request list", pr.ID)
		}
	}
}

func TestReportTaskP4Changelist_RejectsForeignTokenAndUnusableBodies(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "p4 report rejection fixture")
	taskID := seedP4Task(t, issueID, "p4-reject")
	path := "/api/daemon/tasks/" + taskID + "/p4/changelists"

	t.Run("daemon token for another workspace", func(t *testing.T) {
		foreign := "11111111-1111-4111-8111-111111111111"
		testutil.Call(t, testHandler.ReportTaskP4Changelist,
			p4DaemonRequest(http.MethodPost, path, reportBody(795300, 795300, nil), foreign, "p4-daemon-foreign", "taskId", taskID),
		).Want(http.StatusNotFound)
	})

	t.Run("missing port", func(t *testing.T) {
		testutil.Call(t, testHandler.ReportTaskP4Changelist,
			p4DaemonRequest(http.MethodPost, path, reportBody(795301, 795301, map[string]any{"port": ""}), testWorkspaceID, "p4-daemon-a", "taskId", taskID),
		).Want(http.StatusBadRequest)
	})

	t.Run("unknown status", func(t *testing.T) {
		testutil.Call(t, testHandler.ReportTaskP4Changelist,
			p4DaemonRequest(http.MethodPost, path, reportBody(795302, 795302, map[string]any{"status": "submitting"}), testWorkspaceID, "p4-daemon-a", "taskId", taskID),
		).Want(http.StatusBadRequest)
	})

	t.Run("task without an issue", func(t *testing.T) {
		// A chat task resolves a workspace but has no issue to link to.
		agentID := dbfx.Agent(t, "p4 chat agent", handlerTestRuntimeID(t), testutil.Cols{
			"instructions": "",
			"custom_env":   testutil.Raw("'{}'::jsonb"),
			"custom_args":  testutil.Raw("'[]'::jsonb"),
		})
		sessionID := dbfx.ChatSession(t, agentID)
		chatTaskID := dbfx.Task(t, agentID, testutil.Cols{
			"runtime_id":      handlerTestRuntimeID(t),
			"chat_session_id": sessionID,
			"status":          "running",
			"started_at":      testutil.Raw("now()"),
		})
		testutil.Call(t, testHandler.ReportTaskP4Changelist,
			p4DaemonRequest(http.MethodPost, "/api/daemon/tasks/"+chatTaskID+"/p4/changelists",
				reportBody(795303, 795303, nil), testWorkspaceID, "p4-daemon-a", "taskId", chatTaskID),
		).Want(http.StatusBadRequest)
	})
}

// A person pastes a changelist number; everything else stays unknown until a
// daemon picks the row up. The port comes from the issue's project when the
// project names exactly one depot, and the request has to supply it otherwise.
func TestP4ChangelistManualLinkPortResolutionAndUnlink(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("port from the project's only depot", func(t *testing.T) {
		projectID := dbfx.Project(t, "P4 manual link project")
		dbfx.Insert(t, "project_resource", testutil.Cols{
			"project_id":    projectID,
			"workspace_id":  testWorkspaceID,
			"resource_type": "perforce_depot",
			"resource_ref":  `{"port":"` + p4TestPort + `","depot":"//depot/UE"}`,
			"position":      0,
		})
		issueID := dbfx.Issue(t, "p4 manual link", testutil.Cols{"project_id": projectID})

		var created P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+issueID+"/p4-changelists", map[string]any{"number": 794229}, issueID,
		)).Want(http.StatusCreated).JSON(&created)

		if created.Port != p4TestPort {
			t.Errorf("port = %q, want the project depot's %q", created.Port, p4TestPort)
		}
		if created.LinkSource != p4ChangelistLinkManual {
			t.Errorf("link_source = %q, want %q", created.LinkSource, p4ChangelistLinkManual)
		}
		if created.Status != p4ChangelistStatusPending {
			t.Errorf("status = %q, want pending until the daemon polls", created.Status)
		}
		if created.Number != 794229 || created.OriginalNumber != 794229 {
			t.Errorf("numbers = (%d, %d), want (794229, 794229)", created.Number, created.OriginalNumber)
		}
		if got := listP4Changelists(t, issueID); len(got) != 1 {
			t.Fatalf("issue carries %d changelists, want 1", len(got))
		}

		testutil.Call(t, testHandler.UnlinkIssueP4Changelist, p4UserRequest(
			http.MethodDelete, "/api/issues/"+issueID+"/p4-changelists/"+created.ID, nil, issueID,
			"changelistId", created.ID,
		)).Want(http.StatusNoContent)

		if got := listP4Changelists(t, issueID); len(got) != 0 {
			t.Fatalf("issue still carries %d changelists after unlink", len(got))
		}
		// The unlinked changelist held no other issue, so it is gone too and
		// stops appearing in daemon polls.
		if n := dbfx.Count(t, `SELECT count(*) FROM p4_changelist WHERE id = $1`, created.ID); n != 0 {
			t.Errorf("orphan changelist row survived the unlink")
		}

		testutil.Call(t, testHandler.UnlinkIssueP4Changelist, p4UserRequest(
			http.MethodDelete, "/api/issues/"+issueID+"/p4-changelists/"+created.ID, nil, issueID,
			"changelistId", created.ID,
		)).Want(http.StatusNotFound)
	})

	t.Run("422 without a port the project can supply", func(t *testing.T) {
		projectID := dbfx.Project(t, "P4 two-depot project")
		for i, depot := range []string{"//depot/UE", "//depot/Tools"} {
			dbfx.Insert(t, "project_resource", testutil.Cols{
				"project_id":    projectID,
				"workspace_id":  testWorkspaceID,
				"resource_type": "perforce_depot",
				"resource_ref":  `{"port":"` + p4TestPort + `","depot":"` + depot + `"}`,
				"position":      i,
			})
		}
		twoDepots := dbfx.Issue(t, "p4 ambiguous port", testutil.Cols{"project_id": projectID})
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+twoDepots+"/p4-changelists", map[string]any{"number": 794230}, twoDepots,
		)).Want(http.StatusUnprocessableEntity)

		noDepot := dbfx.Issue(t, "p4 no depot")
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+noDepot+"/p4-changelists", map[string]any{"number": 794231}, noDepot,
		)).Want(http.StatusUnprocessableEntity)

		// An explicit port is always enough.
		var created P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+noDepot+"/p4-changelists",
			map[string]any{"number": 794231, "port": p4TestPort}, noDepot,
		)).Want(http.StatusCreated).JSON(&created)
		if created.Port != p4TestPort {
			t.Errorf("port = %q, want %q", created.Port, p4TestPort)
		}
	})

	t.Run("rejects a number that is not a changelist", func(t *testing.T) {
		issueID := dbfx.Issue(t, "p4 bad number")
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
			map[string]any{"number": 0, "port": p4TestPort}, issueID,
		)).Want(http.StatusBadRequest)
	})
}

// The poll list has to reach rows no daemon owns yet — a manual link is one of
// those — and the first daemon that reports on such a row owns it from then on.
func TestP4ChangelistPollListClaimsManualRowsOnce(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := p4ConfiguredProject(t, "p4 claim fixture", p4TestPort)
	issueID := dbfx.Issue(t, "p4 claim fixture", testutil.Cols{"project_id": projectID})
	var manual P4ChangelistResponse
	testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
		http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
		map[string]any{"number": 794300, "port": p4TestPort}, issueID,
	)).Want(http.StatusCreated).JSON(&manual)

	openPath := "/api/daemon/p4/changelists/open?workspace_id=" + testWorkspaceID
	var open struct {
		Changelists []struct {
			ID     string `json:"id"`
			Port   string `json:"port"`
			Status string `json:"status"`
		} `json:"changelists"`
	}
	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon,
		p4DaemonRequest(http.MethodGet, openPath, nil, testWorkspaceID, "p4-daemon-first"),
	).Want(http.StatusOK).JSON(&open)

	found := false
	for _, row := range open.Changelists {
		if row.ID == manual.ID {
			found = true
			if row.Port != p4TestPort || row.Status != p4ChangelistStatusPending {
				t.Errorf("poll row = (%q, %q)", row.Port, row.Status)
			}
		}
	}
	if !found {
		t.Fatalf("manual changelist %s is not in the unowned poll list", manual.ID)
	}

	statusBody := func(status string, over map[string]any) map[string]any {
		update := map[string]any{"id": manual.ID, "status": status}
		for k, v := range over {
			update[k] = v
		}
		return map[string]any{"workspace_id": testWorkspaceID, "updates": []map[string]any{update}}
	}

	var applied struct {
		Updated int `json:"updated"`
	}
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses,
		p4DaemonRequest(http.MethodPost, "/api/daemon/p4/changelists/status",
			statusBody(p4ChangelistStatusShelved, nil), testWorkspaceID, "p4-daemon-first"),
	).Want(http.StatusOK).JSON(&applied)
	if applied.Updated != 1 {
		t.Fatalf("first daemon updated %d rows, want 1", applied.Updated)
	}

	var daemonID string
	dbfx.QueryRow(t, `SELECT daemon_id FROM p4_changelist WHERE id = $1`, manual.ID).Scan(&daemonID)
	if daemonID != "p4-daemon-first" {
		t.Fatalf("daemon_id = %q, want the first reporting daemon", daemonID)
	}

	// A second daemon cannot take over a claimed row, and no longer sees it.
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses,
		p4DaemonRequest(http.MethodPost, "/api/daemon/p4/changelists/status",
			statusBody(p4ChangelistStatusDeleted, nil), testWorkspaceID, "p4-daemon-second"),
	).Want(http.StatusOK).JSON(&applied)
	if applied.Updated != 0 {
		t.Fatalf("second daemon updated %d rows on a claimed changelist, want 0", applied.Updated)
	}
	var open2 struct {
		Changelists []struct {
			ID string `json:"id"`
		} `json:"changelists"`
	}
	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon,
		p4DaemonRequest(http.MethodGet, openPath, nil, testWorkspaceID, "p4-daemon-second"),
	).Want(http.StatusOK).JSON(&open2)
	for _, row := range open2.Changelists {
		if row.ID == manual.ID {
			t.Fatalf("claimed changelist %s is still in another daemon's poll list", manual.ID)
		}
	}

	// Submitting renumbers the change and finishes it, so it leaves the list.
	submittedAt := time.Now().UTC().Truncate(time.Second)
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses,
		p4DaemonRequest(http.MethodPost, "/api/daemon/p4/changelists/status",
			statusBody(p4ChangelistStatusSubmitted, map[string]any{
				"number":       794355,
				"submitted_at": submittedAt.Format(time.RFC3339),
			}), testWorkspaceID, "p4-daemon-first"),
	).Want(http.StatusOK).JSON(&applied)
	if applied.Updated != 1 {
		t.Fatalf("submit update changed %d rows, want 1", applied.Updated)
	}

	after := listP4Changelists(t, issueID)
	if len(after) != 1 {
		t.Fatalf("issue carries %d changelists, want 1", len(after))
	}
	if after[0].Number != 794355 || after[0].OriginalNumber != 794300 {
		t.Errorf("numbers = (%d, %d), want (794355, 794300)", after[0].Number, after[0].OriginalNumber)
	}
	if after[0].SubmittedAt == nil {
		t.Error("submitted changelist has no submitted_at")
	}

	var stillOpen int
	dbfx.QueryRow(t,
		`SELECT count(*) FROM p4_changelist WHERE id = $1 AND status IN ('pending','shelved')`,
		manual.ID,
	).Scan(&stillOpen)
	if stillOpen != 0 {
		t.Error("submitted changelist is still counted as open")
	}
}

func TestP4ChangelistUserRoutesRejectAnonymous(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "p4 anonymous fixture")
	anon := func(method, path string, body any, params ...string) *http.Request {
		req := testutil.JSONRequest(method, path, body)
		return testutil.WithURLParams(req, append([]string{"id", issueID}, params...)...)
	}
	testutil.Call(t, testHandler.ListP4ChangelistsForIssue,
		anon(http.MethodGet, "/api/issues/"+issueID+"/p4-changelists", nil),
	).Want(http.StatusUnauthorized)
	testutil.Call(t, testHandler.LinkIssueP4Changelist,
		anon(http.MethodPost, "/api/issues/"+issueID+"/p4-changelists", map[string]any{"number": 1, "port": p4TestPort}),
	).Want(http.StatusUnauthorized)
	testutil.Call(t, testHandler.UnlinkIssueP4Changelist,
		anon(http.MethodDelete, "/api/issues/"+issueID+"/p4-changelists/33333333-3333-4333-8333-333333333333", nil,
			"changelistId", "33333333-3333-4333-8333-333333333333"),
	).Want(http.StatusUnauthorized)
}

func TestP4ChangelistDaemonRoutesRejectForeignWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	foreign := "22222222-2222-4222-8222-222222222222"

	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon,
		p4DaemonRequest(http.MethodGet, "/api/daemon/p4/changelists/open?workspace_id="+testWorkspaceID,
			nil, foreign, "p4-daemon-foreign"),
	).Want(http.StatusNotFound)

	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses,
		p4DaemonRequest(http.MethodPost, "/api/daemon/p4/changelists/status", map[string]any{
			"workspace_id": testWorkspaceID,
			"updates":      []map[string]any{{"id": "33333333-3333-4333-8333-333333333333", "status": "submitted"}},
		}, foreign, "p4-daemon-foreign"),
	).Want(http.StatusNotFound)

	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon,
		p4DaemonRequest(http.MethodGet, "/api/daemon/p4/changelists/open?workspace_id=not-a-uuid",
			nil, testWorkspaceID, "p4-daemon-a"),
	).Want(http.StatusBadRequest)
}

// Auto-advance is the Perforce-only half of the PR merge automation: it shares
// the workspace target and the per-issue opt-out, and it stands down entirely
// once a pull request is linked.
func TestP4ChangelistAutoAdvance(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setHandlerTestPRMergeStatus(t, "done")

	// Changelist identity is (workspace, server, number), so each case needs a
	// number of its own or two issues would share one row.
	submit := func(t *testing.T, issueID string, number int) {
		t.Helper()
		var cl P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
			map[string]any{"number": number, "port": p4TestPort}, issueID,
		)).Want(http.StatusCreated).JSON(&cl)
		var applied struct {
			Updated int `json:"updated"`
		}
		testutil.Call(t, testHandler.UpdateP4ChangelistStatuses,
			p4DaemonRequest(http.MethodPost, "/api/daemon/p4/changelists/status", map[string]any{
				"workspace_id": testWorkspaceID,
				"updates":      []map[string]any{{"id": cl.ID, "status": "submitted"}},
			}, testWorkspaceID, "p4-daemon-advance"),
		).Want(http.StatusOK).JSON(&applied)
		if applied.Updated != 1 {
			t.Fatalf("status update changed %d rows, want 1", applied.Updated)
		}
	}

	issueStatus := func(t *testing.T, issueID string) string {
		t.Helper()
		var status string
		dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
		return status
	}

	t.Run("every changelist submitted moves the issue", func(t *testing.T) {
		issueID := dbfx.Issue(t, "p4 advance on", testutil.Cols{"status": "in_progress"})
		submit(t, issueID, 794401)
		if got := issueStatus(t, issueID); got != "done" {
			t.Fatalf("issue status = %q, want done", got)
		}
	})

	t.Run("one unsubmitted changelist holds the issue", func(t *testing.T) {
		issueID := dbfx.Issue(t, "p4 advance waiting", testutil.Cols{"status": "in_progress"})
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
			map[string]any{"number": 794500, "port": p4TestPort}, issueID,
		)).Want(http.StatusCreated)
		submit(t, issueID, 794402)
		if got := issueStatus(t, issueID); got != "in_progress" {
			t.Fatalf("issue status = %q, want in_progress while a changelist is pending", got)
		}
	})

	t.Run("the per-issue opt-out stops it", func(t *testing.T) {
		issueID := dbfx.Issue(t, "p4 advance off", testutil.Cols{"status": "in_progress"})
		testutil.Call(t, testHandler.SetIssuePRAutoComplete, p4UserRequest(
			http.MethodPut, "/api/issues/"+issueID+"/pr-auto-complete", map[string]any{"disabled": true}, issueID,
		)).Want(http.StatusOK)
		submit(t, issueID, 794403)
		if got := issueStatus(t, issueID); got != "in_progress" {
			t.Fatalf("issue status = %q, want in_progress with the opt-out on", got)
		}
	})

	t.Run("a linked pull request owns the issue instead", func(t *testing.T) {
		issueID := dbfx.Issue(t, "p4 advance with pr", testutil.Cols{"status": "in_progress"})
		prID := dbfx.Insert(t, "github_pull_request", testutil.Cols{
			"workspace_id":    testWorkspaceID,
			"installation_id": 904001,
			"repo_owner":      "acme",
			"repo_name":       "p4-advance",
			"pr_number":       9041,
			"title":           "Perforce side work",
			"state":           "open",
			"html_url":        "https://github.com/acme/p4-advance/pull/9041",
			"pr_created_at":   testutil.Raw("now()"),
			"pr_updated_at":   testutil.Raw("now()"),
		})
		dbfx.InsertNoID(t, "issue_pull_request",
			testutil.Cols{"issue_id": issueID, "pull_request_id": prID},
			"issue_id = $1 AND pull_request_id = $2", issueID, prID)
		submit(t, issueID, 794404)
		if got := issueStatus(t, issueID); got != "in_progress" {
			t.Fatalf("issue status = %q, want in_progress while a pull request is linked", got)
		}
	})

	t.Run("workspace target none leaves the issue alone", func(t *testing.T) {
		setHandlerTestPRMergeStatus(t, prMergeStatusNone)
		issueID := dbfx.Issue(t, "p4 advance workspace off", testutil.Cols{"status": "in_progress"})
		submit(t, issueID, 794405)
		if got := issueStatus(t, issueID); got != "in_progress" {
			t.Fatalf("issue status = %q, want in_progress with no workspace target", got)
		}
	})
}

// setHandlerTestPRMergeStatus pins the workspace's merge target for one test and
// restores whatever the suite's workspace had.
func setHandlerTestPRMergeStatus(t *testing.T, status string) {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	dbfx.Exec(t, `UPDATE workspace SET settings = $1 WHERE id = $2`,
		[]byte(`{"pr_merge_status":"`+status+`"}`), testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, previous, testWorkspaceID)
	})
}

// Deleting an issue takes its changelists with it. A changelist another issue
// still holds stays, because its description can reference several issues.
func TestP4ChangelistSweptOnIssueDelete(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	soleIssue := dbfx.Issue(t, "p4 delete sole holder")
	sharedIssueA := dbfx.Issue(t, "p4 delete shared a")
	sharedIssueB := dbfx.Issue(t, "p4 delete shared b")

	link := func(issueID string, number int) P4ChangelistResponse {
		var cl P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
			map[string]any{"number": number, "port": p4TestPort}, issueID,
		)).Want(http.StatusCreated).JSON(&cl)
		return cl
	}
	sole := link(soleIssue, 794600)
	shared := link(sharedIssueA, 794601)
	if got := link(sharedIssueB, 794601); got.ID != shared.ID {
		t.Fatalf("second issue linked a new row %s, want the shared %s", got.ID, shared.ID)
	}

	deleteIssue := func(issueID string) error {
		return testHandler.Queries.DeleteIssue(ctx, db.DeleteIssueParams{
			ID:          parseUUID(issueID),
			WorkspaceID: parseUUID(testWorkspaceID),
		})
	}
	if err := deleteIssue(soleIssue); err != nil {
		t.Fatalf("delete sole-holder issue: %v", err)
	}
	if err := deleteIssue(sharedIssueA); err != nil {
		t.Fatalf("delete shared issue: %v", err)
	}

	if n := dbfx.Count(t, `SELECT count(*) FROM p4_changelist WHERE id = $1`, sole.ID); n != 0 {
		t.Error("changelist survived the delete of its only issue")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_p4_changelist WHERE issue_id = $1`, soleIssue); n != 0 {
		t.Error("changelist link survived the issue delete")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM p4_changelist WHERE id = $1`, shared.ID); n != 1 {
		t.Error("changelist another issue still holds was deleted")
	}
	if got := listP4Changelists(t, sharedIssueB); len(got) != 1 || got[0].ID != shared.ID {
		t.Errorf("remaining issue lost its changelist: %+v", got)
	}
}

// Workspace teardown removes both tables. The statement also deletes this
// workspace's pull requests, so it runs inside a transaction that is rolled
// back: the assertion is that the statement itself accepts the new tables.
func TestP4ChangelistSweptOnWorkspaceDelete(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issueID := dbfx.Issue(t, "p4 workspace sweep")
	var cl P4ChangelistResponse
	testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
		http.MethodPost, "/api/issues/"+issueID+"/p4-changelists",
		map[string]any{"number": 794700, "port": p4TestPort}, issueID,
	)).Want(http.StatusCreated).JSON(&cl)

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := testHandler.Queries.WithTx(tx).DeleteWorkspacePullRequests(ctx, parseUUID(testWorkspaceID)); err != nil {
		t.Fatalf("workspace pull-request sweep: %v", err)
	}
	var left int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM p4_changelist WHERE id = $1`, cl.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("changelist still present inside the workspace sweep")
	}
	var links int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM issue_p4_changelist WHERE changelist_id = $1`, cl.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("changelist link still present inside the workspace sweep")
	}
}
