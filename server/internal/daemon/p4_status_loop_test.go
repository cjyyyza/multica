package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4StatusLoopMapsRenumberAndDelete(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "alice")
	var posted p4StatusUpdateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/daemon/p4/changelists/open":
			if r.URL.Query().Get("workspace_id") != "ws-1" {
				t.Errorf("workspace_id = %q", r.URL.Query().Get("workspace_id"))
			}
			json.NewEncoder(w).Encode(p4OpenChangelistsResponse{Changelists: []p4OpenChangelist{
				{ID: "cl-sub", Port: "ssl:p4.example:1666", Number: 795095, OriginalNumber: 795095, Status: "shelved"},
				{ID: "cl-del", Port: "ssl:p4.example:1666", Number: 111, OriginalNumber: 111, Status: "pending"},
			}})
		case r.URL.Path == "/api/daemon/p4/changelists/status":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode status: %v", err)
			}
			json.NewEncoder(w).Encode(p4StatusUpdateResponse{Updated: len(posted.Updates)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	d := &Daemon{
		logger:     slog.Default(),
		client:     NewClient(server.URL),
		p4Cache:    &p4cache.Cache{P4Path: bin},
		workspaces: map[string]*workspaceState{"ws-1": {}},
	}
	d.pollP4ChangelistStatuses(t.Context())
	if len(posted.Updates) != 2 {
		t.Fatalf("updates = %+v", posted.Updates)
	}
	got := map[string]p4StatusUpdate{}
	for _, update := range posted.Updates {
		got[update.ID] = update
	}
	sub := got["cl-sub"]
	if sub.Status != "submitted" || sub.Number != 795115 || sub.SubmittedAt == "" {
		t.Fatalf("submitted update = %+v", sub)
	}
	del := got["cl-del"]
	if del.Status != "deleted" || del.Number != 111 {
		t.Fatalf("deleted update = %+v", del)
	}
}

func TestP4StatusLoopToleratesOldServer(t *testing.T) {
	var log bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	d := &Daemon{
		logger:     slog.New(slog.NewTextHandler(&log, nil)),
		client:     NewClient(server.URL),
		p4Cache:    &p4cache.Cache{P4Path: "p4"},
		workspaces: map[string]*workspaceState{"ws-1": {}},
	}
	d.pollP4ChangelistStatuses(t.Context())
	if !strings.Contains(log.String(), "no changelist route") {
		t.Fatalf("log = %s", log.String())
	}
}

func TestFinishReportsShelvedChangelistAndReview(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_LOG", filepath.Join(t.TempDir(), "p4.log"))
	t.Setenv("P4_FAKE_MUTATE", "ok\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... action edit\n... change 1001\n... type text\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... headType text\n... headRev 2\n... haveRev 2\n... action edit\n... change 1001\n")

	var report p4ChangelistReport
	var linkedSeparately bool
	var reportAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/daemon/tasks/task/p4/changelists":
			reportAuth = r.Header.Get("Authorization")
			if err := json.Unmarshal(body, &report); err != nil {
				t.Errorf("report: %v", err)
			}
			w.Write([]byte(`{}`))
		case "/api/issues/issue-1/swarm-reviews":
			linkedSeparately = true
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	swarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"reviews":[]}`))
			return
		}
		w.Write([]byte(`{"review":{"id":9,"author":"alice","state":"needsReview","description":"refs #5002","changes":[1001]}}`))
	}))
	defer swarm.Close()

	ws := &p4cache.Workspace{
		Port: "ssl:p4.example:1666", User: "alice", Client: "alice_DEV", Charset: "utf8",
		Root: root, Dir: root, Stream: "//DJ01_Proj/Dev", ServerCluster: "p4_dj01",
	}
	binding := &p4TaskBinding{
		ws: ws,
		ref: p4depot.Ref{
			Port: ws.Port, Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
			User: "alice", SwarmURL: swarm.URL,
		},
		ticket: "5002", issueKey: "DJ-123", title: "fix the crash",
		description: "refs #5002 fix the crash\n\nMultica: DJ-123\n",
		change:      "1001", taskID: "task",
	}
	client := NewClient(api.URL)
	client.SetToken("mdt_daemon")
	d := &Daemon{logger: slog.Default(), client: client, p4Cache: &p4cache.Cache{P4Path: bin}}
	d.finishP4Workspace(t.Context(), Task{
		ID: "task", WorkspaceID: "ws", IssueID: "issue-1", AuthToken: "mat_task",
	}, binding)
	// /api/daemon/* accepts daemon credentials only. Sending the agent's task
	// token got a 401 in the live test, and the changelist never reached the
	// issue.
	if reportAuth != "Bearer mdt_daemon" {
		t.Fatalf("changelist report Authorization = %q, want the daemon token", reportAuth)
	}
	if report.Status != "shelved" || report.Number != 1001 || report.OriginalNumber != 1001 {
		t.Fatalf("report = %+v", report)
	}
	if report.ServerKey != "p4_dj01" || report.Client != "alice_DEV" || report.Ticket != "5002" || report.FilesCount != 1 {
		t.Fatalf("report identity = %+v", report)
	}
	if report.SwarmReview == nil || report.SwarmReview.Number != 9 || report.SwarmReview.State != "needsReview" {
		t.Fatalf("swarm = %+v", report.SwarmReview)
	}
	// The server attaches the review from the changelist report; a separate
	// swarm-reviews link would show it twice on the issue.
	if linkedSeparately {
		t.Fatal("the review was also posted to the issue's swarm-reviews route")
	}
	if want := swarm.URL + "/reviews/9"; report.SwarmReview.URL != want {
		t.Fatalf("swarm review url = %q, want %q", report.SwarmReview.URL, want)
	}
}

func TestFinishWarnsWhenTheServerHasNoChangelistRoute(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_MUTATE", "ok\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... action edit\n... change 1001\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... headType text\n... headRev 1\n... haveRev 1\n")
	var log bytes.Buffer
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer api.Close()
	ws := &p4cache.Workspace{
		Port: "ssl:p4.example:1666", User: "alice", Client: "alice_DEV",
		Root: root, Dir: root,
	}
	binding := &p4TaskBinding{
		ws: ws, ref: p4depot.Ref{Port: ws.Port, Depot: "//DJ01_Proj/Dev", NoAutoReview: true},
		ticket: "5002", change: "1001", taskID: "task",
	}
	d := &Daemon{
		logger:  slog.New(slog.NewTextHandler(&log, nil)),
		client:  NewClient(api.URL),
		p4Cache: &p4cache.Cache{P4Path: bin},
	}
	d.finishP4Workspace(t.Context(), Task{ID: "task", AuthToken: "mat_task"}, binding)
	if !strings.Contains(log.String(), "no changelist route") {
		t.Fatalf("log = %s", log.String())
	}
}
