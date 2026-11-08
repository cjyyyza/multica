package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4SwarmDestinationMustBeConfigured(t *testing.T) {
	for _, scope := range []string{"workspace", "task"} {
		for _, requested := range []string{"", "https://trusted.example", "https://untrusted.example"} {
			t.Run(scope+"/"+requested, func(t *testing.T) {
				d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default()}
				configured := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", SwarmURL: "https://trusted.example"}
				if scope == "task" {
					d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(configured)})
				} else {
					d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(configured)})
				}
				ref := configured
				ref.SwarmURL = requested
				wantURL := ""
				if scope == "workspace" {
					wantURL = configured.SwarmURL
				}
				rec := httptest.NewRecorder()
				got, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", ref)
				if requested != "" && requested != wantURL {
					if ok || rec.Code != http.StatusForbidden {
						t.Fatalf("unconfigured destination accepted: %+v, status %d", got, rec.Code)
					}
				} else if !ok || got.SwarmURL != wantURL {
					t.Fatalf("configured destination lost: %+v, status %d", got, rec.Code)
				}
			})
		}
	}
}

func TestP4SwarmCannotSupplyURLForDepotWithoutSwarm(t *testing.T) {
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default()}
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	ref.SwarmURL = "https://untrusted.example"
	rec := httptest.NewRecorder()
	if _, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", ref); ok {
		t.Fatal("task supplied a credential destination that was never configured")
	}
}

func TestP4SwarmCreateDoesNotShelve(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_TICKETS", "p4:1666 (alice) TESTTICKET\n")
	var creates atomic.Int32
	swarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v11/reviews" {
			t.Errorf("unexpected Swarm call: %s %s", r.Method, r.URL.Path)
		}
		creates.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"review":{"id":77,"changes":[1001]}}`))
	}))
	defer swarm.Close()
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", User: "alice", SwarmURL: swarm.URL}
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin, HTTPClient: swarm.Client()}}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(ref)})
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	d.registerActiveRepoCheckoutTask("mat_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
	request := p4SwarmRequest{Port: ref.Port, Depot: ref.Depot, WorkspaceID: "ws", TaskID: "task", WorkDir: root, Action: "create", Changelist: "1001"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/p4/swarm", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer mat_test")
	rec := httptest.NewRecorder()
	d.p4SwarmHandler()(rec, req)
	if rec.Code != http.StatusOK || creates.Load() != 1 {
		t.Fatalf("status=%d creates=%d body=%s", rec.Code, creates.Load(), rec.Body.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "shelve") {
		t.Fatalf("review creation shelved the change: %s", log)
	}
}

func TestP4SwarmUsesBoundDepotDefaults(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_TICKETS", "p4:1666 (alice) TESTTICKET\n")
	var creates atomic.Int32
	swarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v11/reviews" {
			t.Errorf("unexpected Swarm call: %s %s", r.Method, r.URL.Path)
		}
		creates.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"review":{"id":77,"changes":[1001]}}`))
	}))
	defer swarm.Close()
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", User: "alice", Charset: "utf8", SwarmURL: swarm.URL}
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin, HTTPClient: swarm.Client()}}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(ref)})
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	d.rememberP4Binding(&p4TaskBinding{ws: &p4cache.Workspace{Port: ref.Port, User: ref.User, Charset: ref.Charset, Dir: root}, ref: ref, taskID: "task"})
	d.registerActiveRepoCheckoutTask("mat_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
	request := p4SwarmRequest{WorkspaceID: "ws", TaskID: "task", WorkDir: root, Action: "create", Changelist: "1001"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/p4/swarm", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer mat_test")
	rec := httptest.NewRecorder()
	d.p4SwarmHandler()(rec, req)
	if rec.Code != http.StatusOK || creates.Load() != 1 {
		t.Fatalf("status=%d creates=%d body=%s", rec.Code, creates.Load(), rec.Body.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "shelve") {
		t.Fatalf("review creation shelved the change: %s", log)
	}
}

func TestP4ChangelistLinkReportsVerifiedNativeChange(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... oldChange 1001\n... status pending\n... client alice_DEV\n... description Fix the editor crash\n... depotFile0 //depot/UE/Source/A.cpp\n")
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... depotFile0 //depot/UE/Source/A.cpp\n")
	var report p4ChangelistReport
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/tasks/task/p4/changelists" {
			t.Errorf("unexpected report path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":1001,"status":"shelved"}`))
	}))
	defer api.Close()
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", User: "alice"}
	ws := &p4cache.Workspace{Port: ref.Port, User: "alice", Client: "alice_DEV", Charset: "utf8", ServerCluster: "cluster", Root: root, Dir: root}
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}, client: NewClient(api.URL)}
	d.rememberP4Binding(&p4TaskBinding{ws: ws, ref: ref, ticket: "123", taskID: "task"})
	d.registerActiveRepoCheckoutTask("mat_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
	body, _ := json.Marshal(p4ChangelistLinkRequest{WorkspaceID: "ws", TaskID: "task", WorkDir: root, Changelist: "1001"})
	req := httptest.NewRequest(http.MethodPost, "/p4/changelist/link", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer mat_test")
	rec := httptest.NewRecorder()
	d.p4ChangelistLinkHandler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if report.Number != 1001 || report.Status != "shelved" || report.Client != "alice_DEV" || report.ServerKey != "cluster" || report.FilesCount != 1 || report.Ticket != "123" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestP4LegacyEndpointExplainsVersionSkew(t *testing.T) {
	rec := httptest.NewRecorder()
	legacyP4Handler()(rec, httptest.NewRequest(http.MethodPost, "/p4/run", nil))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "upgrade the multica CLI") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
