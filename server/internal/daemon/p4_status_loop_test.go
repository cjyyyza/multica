package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4StatusPollRefusesUnconfiguredServer(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_LOG", logPath)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/open") {
			json.NewEncoder(w).Encode(p4OpenChangelistsResponse{Changelists: []p4OpenChangelist{{
				ID: "unconfigured", Port: "rsh:inert-test-sentinel", Number: 1001, Status: "pending",
			}}})
			return
		}
		t.Error("an unconfigured changelist must not be observed or claimed")
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.pollP4ChangelistStatuses(t.Context())
	if log, _ := os.ReadFile(logPath); len(log) != 0 {
		t.Fatalf("unconfigured server reached the p4 executable: %s", log)
	}
}

func TestP4StatusLoopMapsRenumberAndDelete(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_INFO", "... serverCluster p4_cluster\n")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 795115\n... oldChange 795095\n... status submitted\n... time 1700000000\n")
	var posted p4StatusUpdateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/daemon/p4/changelists/open":
			if r.URL.Query().Get("workspace_id") != "ws-1" {
				t.Errorf("workspace_id = %q", r.URL.Query().Get("workspace_id"))
			}
			json.NewEncoder(w).Encode(p4OpenChangelistsResponse{P4Depots: []P4DepotData{{Port: "ssl:p4.example:1666", Depot: "//depot"}}, Changelists: []p4OpenChangelist{
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
	if sub.Status != "submitted" || sub.Number != 795115 || sub.OriginalNumber != 795095 || sub.ServerKey != "p4_cluster" || sub.SubmittedAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("submitted update = %+v", sub)
	}
	del := got["cl-del"]
	if del.Status != "deleted" || del.Number != 111 {
		t.Fatalf("deleted update = %+v", del)
	}
}

func TestP4StatusPollUsesPersistedConnectionContext(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4USER", "unrelated-default")
	t.Setenv("P4_FAKE_INFO", "... serverCluster cluster\n")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status pending\n")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/open") {
			// Use the wire shape so an older daemon struct silently dropping
			// these fields reproduces the wrong-account regression.
			io.WriteString(w, `{"p4_depots":[{"port":"p4:1666","depot":"//depot"}],"changelists":[{"id":"cl","port":"p4:1666","number":1001,"status":"pending","p4_user":"stored-user","charset":"utf8"}]}`)
			return
		}
		io.WriteString(w, `{"updated":1}`)
	}))
	defer api.Close()
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.pollP4ChangelistStatuses(t.Context())
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, "-u stored-user") || !strings.Contains(line, "-C utf8") {
			t.Errorf("poll lost its persisted connection context: %s", line)
		}
	}
}

func TestP4StatusPollRejectsInvalidSubmissionTime(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_INFO", "... serverCluster cluster\n")
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
	for _, value := range []string{"", "not-a-time", "0", "-1", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status submitted\n... time "+value+"\n")
			update, ok := d.observeP4Changelist(t.Context(), p4OpenChangelist{Number: 1001}, p4depot.Ref{Port: "p4:1666", Depot: "//depot"})
			if ok {
				t.Fatalf("fabricated a submitted timestamp from %q: %+v", value, update)
			}
		})
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
		// DJ01's Swarm 2024.2 shapes: the v9 list filtered by change[], and the
		// v11 create answer with the review as a one-element array.
		if r.Method == http.MethodGet {
			if r.URL.Path != "/api/v9/reviews" {
				t.Errorf("review lookup = %s, want the v9 list that honors change[]", r.URL.Path)
			}
			w.Write([]byte(`{"reviews":[],"totalCount":0}`))
			return
		}
		w.Write([]byte(`{"error":null,"messages":[],"data":{"review":[{"id":9,"author":"alice","state":"needsReview","description":"refs #5002","changes":[1001]}]}}`))
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
		change:      "1001", taskID: "task", workspaceID: "ws",
	}
	client := NewClient(api.URL)
	client.SetToken("mdt_daemon")
	d := &Daemon{logger: slog.Default(), client: client, p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(binding.ref)})
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

func TestFinishFailsWhenTheServerHasNoChangelistRoute(t *testing.T) {
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
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task", AuthToken: "mat_task"}, binding); err == nil {
		t.Fatal("missing server route was treated as a delivered changelist")
	}
}

func TestP4PollRefUsesOnlyConfiguredCredentialsAndReviewOrigin(t *testing.T) {
	depots := []P4DepotData{{Port: "ssl:p4.example:1666", Depot: "//depot", User: "configured", Charset: "utf8", SwarmURL: "https://swarm.example"}}
	cl := p4OpenChangelist{Port: "ssl:p4.example:1666", P4User: "saved", Charset: "utf8-bom", SwarmURL: "https://foreign.example"}
	ref, ok := p4PollRef(depots, cl)
	if !ok || ref.User != "configured" || ref.Charset != "utf8" || ref.SwarmURL != "" {
		t.Fatalf("unsafe poll ref: %+v %v", ref, ok)
	}
	cl.SwarmURL = "https://swarm.example"
	ref, ok = p4PollRef(depots, cl)
	if !ok || ref.SwarmURL != "https://swarm.example" {
		t.Fatalf("configured Swarm lost: %+v %v", ref, ok)
	}
	if _, ok := p4PollRef(depots, p4OpenChangelist{Port: "other.example:1666", P4User: "saved", Charset: "utf8"}); ok {
		t.Fatal("accepted another server")
	}
	if _, ok := p4PollRef([]P4DepotData{{Port: "rsh:inert", Depot: "//depot"}}, p4OpenChangelist{Port: "rsh:inert", P4User: "saved"}); ok {
		t.Fatal("background polling may not start an rsh process")
	}
}

func TestP4PollRefRejectsMalformedPersistedContext(t *testing.T) {
	depots := []P4DepotData{{Port: "p4:1666", Depot: "//depot"}}
	for _, cl := range []p4OpenChangelist{
		{Port: "p4:1666", P4User: "two users"},
		{Port: "p4:1666", Charset: "utf8\nother"},
	} {
		if ref, ok := p4PollRef(depots, cl); ok {
			t.Errorf("accepted malformed stored connection context: %+v", ref)
		}
	}
}
