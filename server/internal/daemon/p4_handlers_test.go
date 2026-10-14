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
				rec := httptest.NewRecorder()
				got, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", ref)
				if requested == "https://untrusted.example" {
					if ok || rec.Code != http.StatusForbidden {
						t.Fatalf("unconfigured credential destination accepted: %+v, status %d", got, rec.Code)
					}
				} else if !ok || got.SwarmURL != configured.SwarmURL {
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

func TestP4HandlersRejectUnboundWritesEvenWithOldCheckout(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: filepath.Join(root, "missing-p4")}}
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", User: "alice"}
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	d.registerActiveRepoCheckoutTask("mat_p4_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
	checkout := filepath.Join(root, "p4", "mc-legacy-task-client")
	if err := os.MkdirAll(checkout, 0755); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"edit", "submit", "sync"} {
		body, _ := json.Marshal(map[string]any{"workspace_id": "ws", "task_id": "task", "workdir": checkout, "port": ref.Port, "depot": ref.Depot, "action": action, "changelist": "1001"})
		req := httptest.NewRequest(http.MethodPost, "/p4/run", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer mat_p4_test")
		rec := httptest.NewRecorder()
		handler := d.p4RunHandler()
		if action == "sync" {
			handler = d.p4SyncHandler()
		}
		handler(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "action_not_allowed") {
			t.Fatalf("unbound %s with old checkout: %d %s", action, rec.Code, rec.Body.String())
		}
	}
}

func TestP4SwarmShelvesOnlyTheBoundChange(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, tc := range []struct {
		name, change string
		bound        bool
		want         int
	}{
		{"no binding", "1001", false, http.StatusConflict},
		{"other change", "9999", true, http.StatusConflict},
		{"pending task change", "1001", true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "p4.log")
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
			d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
			d.registerActiveRepoCheckoutTask("mat_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
			if tc.bound {
				d.rememberP4Binding(&p4TaskBinding{
					ws:  &p4cache.Workspace{Port: ref.Port, Client: "alice_main", User: "alice", Root: root, Dir: root},
					ref: ref, taskID: "task", change: "1001",
				})
			}
			body, _ := json.Marshal(p4SwarmRequest{Port: ref.Port, Depot: ref.Depot, WorkspaceID: "ws", TaskID: "task", WorkDir: root, Action: "create", Changelist: tc.change, Shelve: true})
			req := httptest.NewRequest(http.MethodPost, "/p4/swarm", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer mat_test")
			rec := httptest.NewRecorder()
			d.p4SwarmHandler()(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			log, err := os.ReadFile(logPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.want != http.StatusOK {
				if creates.Load() != 0 || len(log) != 0 {
					t.Fatalf("rejected request reached P4/Swarm: %s", log)
				}
				return
			}
			if creates.Load() != 1 || !strings.Contains(string(log), "shelve -r -c 1001") {
				t.Fatalf("wrong shelf/review behavior: creates=%d calls=%s", creates.Load(), log)
			}
		})
	}
}
