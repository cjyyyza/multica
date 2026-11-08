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
			_ = json.NewEncoder(w).Encode(p4OpenChangelistsResponse{Changelists: []p4OpenChangelist{{ID: "unconfigured", Port: "rsh:inert-test-sentinel", Number: 1001, Status: "pending"}}})
			return
		}
		t.Error("an unconfigured changelist must not be observed or claimed")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.pollP4ChangelistStatuses(t.Context())
	if log, _ := os.ReadFile(logPath); len(log) != 0 {
		t.Fatalf("unconfigured server reached p4: %s", log)
	}
}

func TestP4StatusLoopMapsRenumberAndDelete(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_INFO", "... serverCluster p4_cluster\n")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 795115\n... oldChange 795095\n... status submitted\n... time 1700000000\n")
	var posted p4StatusUpdateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/p4/changelists/open":
			_ = json.NewEncoder(w).Encode(p4OpenChangelistsResponse{P4Depots: []P4DepotData{{Port: "ssl:p4.example:1666", Depot: "//depot"}}, Changelists: []p4OpenChangelist{
				{ID: "cl-sub", Port: "ssl:p4.example:1666", Number: 795095, OriginalNumber: 795095, Status: "shelved"},
				{ID: "cl-del", Port: "ssl:p4.example:1666", Number: 111, OriginalNumber: 111, Status: "pending"},
			}})
		case "/api/daemon/p4/changelists/status":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode status: %v", err)
			}
			_ = json.NewEncoder(w).Encode(p4StatusUpdateResponse{Updated: len(posted.Updates)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d := &Daemon{logger: slog.Default(), client: NewClient(server.URL), p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws-1": {}}}
	d.pollP4ChangelistStatuses(t.Context())
	if len(posted.Updates) != 2 {
		t.Fatalf("updates=%+v", posted.Updates)
	}
	got := map[string]p4StatusUpdate{}
	for _, update := range posted.Updates {
		got[update.ID] = update
	}
	if sub := got["cl-sub"]; sub.Status != "submitted" || sub.Number != 795115 || sub.OriginalNumber != 795095 || sub.ServerKey != "p4_cluster" || sub.SubmittedAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("submitted update=%+v", sub)
	}
	if del := got["cl-del"]; del.Status != "deleted" || del.Number != 111 {
		t.Fatalf("deleted update=%+v", del)
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
			_, _ = io.WriteString(w, `{"p4_depots":[{"port":"p4:1666","depot":"//depot"}],"changelists":[{"id":"cl","port":"p4:1666","number":1001,"status":"pending","p4_user":"stored-user","charset":"utf8"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"updated":1}`)
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
			t.Errorf("poll lost persisted connection context: %s", line)
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
				t.Fatalf("fabricated timestamp from %q: %+v", value, update)
			}
		})
	}
}

func TestP4StatusLoopToleratesOldServer(t *testing.T) {
	var log bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	d := &Daemon{logger: slog.New(slog.NewTextHandler(&log, nil)), client: NewClient(server.URL), p4Cache: &p4cache.Cache{P4Path: "p4"}, workspaces: map[string]*workspaceState{"ws-1": {}}}
	d.pollP4ChangelistStatuses(t.Context())
	if !strings.Contains(log.String(), "no changelist route") {
		t.Fatalf("log=%s", log.String())
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
	for _, cl := range []p4OpenChangelist{{Port: "p4:1666", P4User: "two users"}, {Port: "p4:1666", Charset: "utf8\nother"}} {
		if ref, ok := p4PollRef(depots, cl); ok {
			t.Errorf("accepted malformed context: %+v", ref)
		}
	}
}
