package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestP4PollListPreservesReportedConnectionContext(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := p4ConfiguredProject(t, "poll connection context", p4TestPort)
	issueID := dbfx.Issue(t, "poll connection context", testutil.Cols{"project_id": projectID})
	taskID := seedP4Task(t, issueID, "poll connection context")
	var saved P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(
		http.MethodPost, "/api/daemon/tasks/"+taskID+"/p4/changelists",
		reportBody(891001, 891001, map[string]any{"p4_user": "workspace-user", "charset": "utf8-bom"}),
		testWorkspaceID, "context-daemon", "taskId", taskID,
	)).Want(http.StatusOK).JSON(&saved)
	var out struct {
		Changelists []struct {
			ID      string `json:"id"`
			P4User  string `json:"p4_user"`
			Charset string `json:"charset"`
		} `json:"changelists"`
	}
	testutil.Call(t, testHandler.ListOpenP4ChangelistsForDaemon, p4DaemonRequest(
		http.MethodGet, "/api/daemon/p4/changelists/open?workspace_id="+testWorkspaceID,
		nil, testWorkspaceID, "context-daemon",
	)).Want(http.StatusOK).JSON(&out)
	found := false
	for _, row := range out.Changelists {
		if row.ID == saved.ID {
			if row.P4User != "workspace-user" || row.Charset != "utf8-bom" {
				t.Fatalf("persisted context lost on poll readback: %+v", row)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("reported changelist is missing from the configured poll list")
	}
	const submittedAt = "2020-01-02T03:04:05Z"
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(
		http.MethodPost, "/api/daemon/p4/changelists/status", map[string]any{
			"workspace_id": testWorkspaceID,
			"updates":      []map[string]any{{"id": saved.ID, "status": "submitted", "submitted_at": submittedAt}},
		}, testWorkspaceID, "context-daemon",
	)).Want(http.StatusOK)
	after := listP4Changelists(t, issueID)
	if len(after) != 1 || after[0].SubmittedAt == nil {
		t.Fatalf("poll submission time did not survive persistence: %+v", after)
	}
	got, err := time.Parse(time.RFC3339, *after[0].SubmittedAt)
	want, _ := time.Parse(time.RFC3339, submittedAt)
	if err != nil || !got.Equal(want) {
		t.Fatalf("submission instant = %q, want %q: %v", *after[0].SubmittedAt, submittedAt, err)
	}
}

func TestP4TaskReportPreservesServerSubmissionTimeOnRetry(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	row := p4PollLeaseFixture(t, "timestamp-owner")
	const submittedAt = "2020-01-02T03:04:05Z"
	for attempt := 0; attempt < 2; attempt++ {
		var saved P4ChangelistResponse
		testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost, "/",
			reportBody(int(row.Number), int(row.OriginalNumber), map[string]any{"status": "submitted", "submitted_at": submittedAt}),
			testWorkspaceID, "timestamp-owner", "taskId", *row.TaskID)).Want(http.StatusOK).JSON(&saved)
		if saved.SubmittedAt == nil {
			t.Fatal("submission receipt lost its timestamp")
		}
		got, err := time.Parse(time.RFC3339, *saved.SubmittedAt)
		want, _ := time.Parse(time.RFC3339, submittedAt)
		if err != nil || !got.Equal(want) {
			t.Fatalf("retry %d replaced server submission time: %s (%v)", attempt, *saved.SubmittedAt, err)
		}
	}
	testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost, "/",
		reportBody(int(row.Number), int(row.OriginalNumber), map[string]any{"status": "submitted", "submitted_at": "not-a-timestamp"}),
		testWorkspaceID, "timestamp-owner", "taskId", *row.TaskID)).Want(http.StatusBadRequest)
}
