package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

var p4LeaseTestNumber atomic.Int32

func p4PollLeaseFixture(t *testing.T, owner string) P4ChangelistResponse {
	t.Helper()
	project := p4ConfiguredProject(t, "poll lease", p4TestPort)
	issue := dbfx.Issue(t, "poll lease", testutil.Cols{"project_id": project})
	task := seedP4Task(t, issue, "poll lease")
	var row P4ChangelistResponse
	number := 892000 + int(p4LeaseTestNumber.Add(1))
	testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost, "/", reportBody(number, number, nil), testWorkspaceID, owner, "taskId", task)).Want(http.StatusOK).JSON(&row)
	return row
}

func claimP4PollLease(t *testing.T, row P4ChangelistResponse, daemon string, generation int64, status int) int64 {
	t.Helper()
	var out struct {
		Generation int64 `json:"poll_generation"`
	}
	req := p4DaemonRequest(http.MethodPost, "/", map[string]any{"workspace_id": testWorkspaceID, "poll_generation": generation}, testWorkspaceID, daemon, "changelistId", row.ID)
	result := testutil.Call(t, testHandler.ClaimP4ChangelistPoll, req).Want(status)
	if status == http.StatusOK {
		result.JSON(&out)
	}
	return out.Generation
}

func TestP4PollLeaseTakeoverFencesOldObservations(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "old-owner")
	claimP4PollLease(t, row, "new-owner", 0, http.StatusConflict)
	dbfx.Exec(t, "UPDATE p4_changelist SET updated_at = now() - interval '10 minutes' WHERE id = $1", row.ID)
	first := claimP4PollLease(t, row, "new-owner", 0, http.StatusOK)
	if first != 1 {
		t.Fatalf("first generation = %d", first)
	}
	claimP4PollLease(t, row, "another-owner", 0, http.StatusConflict)
	second := claimP4PollLease(t, row, "new-owner", first, http.StatusOK)
	for _, tc := range []struct {
		owner      string
		generation int64
		want       int
	}{{"old-owner", 0, 0}, {"new-owner", first, 0}, {"new-owner", second, 1}} {
		var out struct {
			Updated int `json:"updated"`
		}
		testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(http.MethodPost, "/", map[string]any{
			"workspace_id": testWorkspaceID, "updates": []map[string]any{{"id": row.ID, "status": "submitted", "submitted_at": "2020-01-02T03:04:05Z", "poll_generation": tc.generation}},
		}, testWorkspaceID, tc.owner)).Want(http.StatusOK).JSON(&out)
		if out.Updated != tc.want {
			t.Fatalf("owner=%s generation=%d updated=%d want=%d", tc.owner, tc.generation, out.Updated, tc.want)
		}
	}
	claimP4PollLease(t, row, "new-owner", second, http.StatusConflict)
}

func TestP4PollLeaseDoesNotStealAnOnlineOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "online-owner")
	dbfx.Exec(t, "UPDATE p4_changelist SET updated_at = now() - interval '10 minutes' WHERE id = $1", row.ID)
	runtime := dbfx.Runtime(t, "P4 online owner", testutil.Cols{"daemon_id": "online-owner"})
	claimP4PollLease(t, row, "new-owner", 0, http.StatusConflict)
	dbfx.Exec(t, "UPDATE agent_runtime SET last_seen_at = now() - interval '10 minutes' WHERE id = $1", runtime)
	claimP4PollLease(t, row, "new-owner", 0, http.StatusOK)
}

func TestP4TaskReportFencesAnEarlierPoll(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "owner")
	claimed := claimP4PollLease(t, row, "owner", 0, http.StatusOK)
	if row.TaskID == nil {
		t.Fatal("task report did not retain its task")
	}
	var reported P4ChangelistResponse
	testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost, "/",
		reportBody(int(row.Number), int(row.OriginalNumber), map[string]any{"status": "shelved", "fence_polls": true}),
		testWorkspaceID, "owner", "taskId", *row.TaskID)).Want(http.StatusOK).JSON(&reported)
	var out struct {
		Updated int `json:"updated"`
	}
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(http.MethodPost, "/", map[string]any{
		"workspace_id": testWorkspaceID, "updates": []map[string]any{{"id": row.ID, "status": "pending", "poll_generation": claimed}},
	}, testWorkspaceID, "owner")).Want(http.StatusOK).JSON(&out)
	if out.Updated != 0 {
		t.Fatal("an observation from before the task's shelf report overwrote that report")
	}
	var status string
	if err := testPool.QueryRow(t.Context(), "SELECT status FROM p4_changelist WHERE id = $1", row.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "shelved" || reported.ID != row.ID {
		t.Fatalf("new task receipt was not preserved: status=%s reported=%s", status, reported.ID)
	}
	claimP4PollLease(t, row, "owner", claimed+1, http.StatusOK)
}

func TestP4LegacyTaskReportCannotOverwriteLeasedGeneration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "owner")
	generation := claimP4PollLease(t, row, "owner", 0, http.StatusOK)
	if row.TaskID == nil {
		t.Fatal("task report did not retain its task")
	}
	for _, owner := range []string{"owner", "legacy-owner"} {
		testutil.Call(t, testHandler.ReportTaskP4Changelist, p4DaemonRequest(http.MethodPost, "/",
			reportBody(int(row.Number), int(row.OriginalNumber), map[string]any{"status": "shelved", "description": "legacy overwrite"}),
			testWorkspaceID, owner, "taskId", *row.TaskID)).Want(http.StatusConflict)
	}
	var owner, status, description string
	var actualGeneration int64
	if err := testPool.QueryRow(t.Context(), "SELECT daemon_id, status, description, poll_generation FROM p4_changelist WHERE id = $1", row.ID).Scan(&owner, &status, &description, &actualGeneration); err != nil {
		t.Fatal(err)
	}
	if owner != "owner" || status != row.Status || description != row.Description || actualGeneration != generation {
		t.Fatalf("legacy report overwrote lease-owned data: owner=%s status=%s description=%q generation=%d", owner, status, description, actualGeneration)
	}
	var out struct {
		Updated int `json:"updated"`
	}
	testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(http.MethodPost, "/", map[string]any{
		"workspace_id": testWorkspaceID, "updates": []map[string]any{{"id": row.ID, "status": "shelved", "poll_generation": generation}},
	}, testWorkspaceID, "owner")).Want(http.StatusOK).JSON(&out)
	if out.Updated != 1 {
		t.Fatal("legacy report stranded the lease holder")
	}
}

func TestP4PollLeaseConcurrentTakeoverHasOneWinner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "offline-owner")
	dbfx.Exec(t, "UPDATE p4_changelist SET updated_at = now() - interval '10 minutes' WHERE id = $1", row.ID)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := p4DaemonRequest(http.MethodPost, "/", map[string]any{"workspace_id": testWorkspaceID, "poll_generation": 0}, testWorkspaceID, fmt.Sprintf("candidate-%d", i), "changelistId", row.ID)
			w := httptest.NewRecorder()
			testHandler.ClaimP4ChangelistPoll(w, r)
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for result := range results {
		counts[result]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("claim results = %v", counts)
	}
}

func TestP4PollLeaseRejectsMalformedAndUnconfiguredClaims(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	row := p4PollLeaseFixture(t, "owner")
	claimP4PollLease(t, row, "owner", -1, http.StatusBadRequest)
	dbfx.Exec(t, "UPDATE p4_changelist SET port = 'foreign.example:1666', server_key = '', client = '' WHERE id = $1", row.ID)
	claimP4PollLease(t, row, "owner", 0, http.StatusForbidden)
}
