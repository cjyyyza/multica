package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func p4ConfiguredProject(t *testing.T, label, port string) string {
	t.Helper()
	projectID := dbfx.Project(t, label)
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": projectID, "workspace_id": testWorkspaceID, "resource_type": "perforce_depot",
		"resource_ref": fmt.Sprintf(`{"port":%q,"depot":"//depot/UE","user":"configured","charset":"utf8"}`, port), "position": 0,
	})
	return projectID
}

func TestP4ChangelistManualAndTaskIdentityConverge(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, manualFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_first_%t", manualFirst), func(t *testing.T) {
			projectID := p4ConfiguredProject(t, "identity project", p4TestPort)
			issueID := dbfx.Issue(t, "identity issue", testutil.Cols{"project_id": projectID})
			taskID := seedP4Task(t, issueID, "identity")
			number := 890100
			if manualFirst {
				number++
			}
			var manual, reported P4ChangelistResponse
			link := func(number int, into *P4ChangelistResponse) {
				testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(http.MethodPost,
					"/api/issues/"+issueID+"/p4-changelists", map[string]any{"number": number}, issueID,
				)).Want(http.StatusCreated).JSON(into)
			}
			report := func(current int, status string) {
				testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost,
					"/api/daemon/tasks/"+taskID+"/p4/changelists", reportBody(current, number, map[string]any{"status": status}),
					testWorkspaceID, "identity-daemon", "taskId", taskID,
				)).Want(http.StatusOK).JSON(&reported)
			}
			if manualFirst {
				link(number, &manual)
			}
			report(number, "shelved")
			if !manualFirst {
				link(number, &manual)
			}
			if manual.ID != reported.ID {
				t.Fatalf("manual %s and reported %s are different rows", manual.ID, reported.ID)
			}
			report(number+100, "submitted")
			link(number+100, &manual)
			if manual.ID != reported.ID || manual.Status != "submitted" || manual.FilesCount != 2 || manual.TaskID == nil {
				t.Fatalf("linking the submitted number lost identity/metadata: %+v", manual)
			}
			// A delayed durable receipt must not undo an observed submission.
			report(number, "shelved")
			if reported.Status != "submitted" || reported.Number != int32(number+100) {
				t.Fatalf("stale replay regressed the submitted change: %+v", reported)
			}
			got := listP4Changelists(t, issueID)
			if len(got) != 1 || (manualFirst && got[0].LinkSource != "manual") {
				t.Fatalf("duplicate rows or lost manual attribution: %+v", got)
			}
		})
	}
}

func TestP4ChangelistPollMergesPortAliasesAndRenumberedManualLink(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "canonical issue")
	taskID := seedP4Task(t, issueID, "canonical")
	var canonical, manual P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost,
		"/api/daemon/tasks/"+taskID+"/p4/changelists", reportBody(890301, 890300, map[string]any{"status": "submitted"}),
		testWorkspaceID, "canonical-daemon", "taskId", taskID,
	)).Want(http.StatusOK).JSON(&canonical)
	otherIssue := dbfx.Issue(t, "manually linked alias")
	testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(http.MethodPost,
		"/api/issues/"+otherIssue+"/p4-changelists", map[string]any{"port": "edge.example:1666", "number": 890301}, otherIssue,
	)).Want(http.StatusCreated).JSON(&manual)
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(http.MethodPost,
		"/api/daemon/p4/changelists/status", map[string]any{"workspace_id": testWorkspaceID, "updates": []map[string]any{{
			"id": manual.ID, "number": 890301, "original_number": 890300, "server_key": "p4_test_cluster", "status": "submitted",
		}}}, testWorkspaceID, "manual-daemon",
	)).Want(http.StatusOK)
	got := listP4Changelists(t, otherIssue)
	if len(got) != 1 || got[0].ID != canonical.ID || got[0].LinkSource != "manual" || got[0].FilesCount != 2 {
		t.Fatalf("manual link did not converge onto canonical metadata: %+v", got)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM p4_changelist WHERE id = $1`, manual.ID); n != 0 {
		t.Fatal("duplicate placeholder survived")
	}
	var owner string
	dbfx.QueryRow(t, `SELECT daemon_id FROM p4_changelist WHERE id = $1`, canonical.ID).Scan(&owner)
	if owner != "canonical-daemon" {
		t.Fatalf("canonical ownership was stolen: %s", owner)
	}
}

func TestP4ChangelistConcurrentManualAndTaskReport(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "concurrent link")
	taskID := seedP4Task(t, issueID, "concurrent link")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, manual := range []bool{true, false} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			if manual {
				testHandler.LinkIssueP4Changelist(rec, p4UserRequest(http.MethodPost, "/",
					map[string]any{"port": p4TestPort, "number": 890400}, issueID))
			} else {
				testHandler.ReportTaskP4Changelist(rec, p4DaemonRequest(http.MethodPost, "/",
					reportBody(890400, 890400, nil), testWorkspaceID, "concurrent-daemon", "taskId", taskID))
			}
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Errorf("concurrent write: %d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := listP4Changelists(t, issueID); len(got) != 1 {
		t.Fatalf("concurrent writes created %d cards", len(got))
	}
}

func TestP4ChangelistPollOnlyExposesConfiguredResources(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := p4ConfiguredProject(t, "poll resource", p4TestPort)
	issueID := dbfx.Issue(t, "poll resource issue", testutil.Cols{"project_id": project})
	ids := map[string]string{}
	for i, port := range []string{p4TestPort, "foreign.example:1666", "rsh:inert-sentinel"} {
		var cl P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(http.MethodPost, "/",
			map[string]any{"port": port, "number": 890500 + i}, issueID,
		)).Want(http.StatusCreated).JSON(&cl)
		ids[port] = cl.ID
	}
	var out struct {
		Changelists []struct{ ID, Port string }
		P4Depots    []P4DepotData `json:"p4_depots"`
	}
	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon, p4DaemonRequest(http.MethodGet,
		"/api/daemon/p4/changelists/open?workspace_id="+testWorkspaceID, nil, testWorkspaceID, "poll-daemon",
	)).Want(http.StatusOK).JSON(&out)
	found := false
	for _, cl := range out.Changelists {
		if cl.ID == ids[p4TestPort] {
			found = true
		}
		if cl.ID == ids["foreign.example:1666"] || cl.ID == ids["rsh:inert-sentinel"] {
			t.Fatalf("unconfigured server was exposed to background polling: %+v", cl)
		}
	}
	if !found {
		t.Fatal("configured project CL was not exposed")
	}
	for _, depot := range out.P4Depots {
		if strings.HasPrefix(depot.Port, "rsh:") {
			t.Fatal("poll exposed an executable P4PORT")
		}
	}
}
