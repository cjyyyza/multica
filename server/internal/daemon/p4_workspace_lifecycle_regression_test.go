package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4RevertProtectsInitialOpens(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, unchanged := range []bool{false, true} {
		name := "explicit file"
		if unchanged {
			name = "unchanged without files"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "A.cpp")
			if err := os.WriteFile(file, []byte("human content"), 0600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "p4.log")
			t.Setenv("P4_FAKE_LOG", logPath)
			t.Setenv("P4_FAKE_DEPOT", "//depot/main")
			t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/A.cpp\n... change 1001\n... action edit\n")
			t.Setenv("P4_FAKE_FSTAT", "... depotFile //depot/main/A.cpp\n... clientFile "+file+"\n... change 1001\n... action edit\n")
			b := &p4TaskBinding{
				ws:  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
				ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main"}, change: "1001",
				initialOpened: map[string]p4OpenedFileSnapshot{"//depot/main/A.cpp": {path: file, action: "edit", exists: true}},
			}
			b.touchPaths([]string{file})
			req := p4MutateRequest{Action: "revert", RevertUnchanged: unchanged}
			if !unchanged {
				req.Files = []string{"A.cpp"}
			}
			_, rej, err := (&Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}).runWorkspaceP4(t.Context(), b, req, root)
			if err != nil {
				t.Fatal(err)
			}
			if !unchanged && (rej == nil || rej.Code != p4CodeOpenedByYou) {
				t.Errorf("initial open accepted: %+v", rej)
			}
			log, _ := os.ReadFile(logPath)
			if strings.Contains(string(log), "revert ") {
				t.Fatalf("human open reached revert:\n%s", log)
			}
		})
	}
}

func TestP4SnapshotDetectsRemovedOpen(t *testing.T) {
	before := map[string]p4OpenedFileSnapshot{"//depot/main/A.cpp": {action: "edit", exists: true}}
	changed := changedP4OpenedFiles(before, map[string]p4OpenedFileSnapshot{})
	if len(changed) != 1 || changed[0] != "//depot/main/A.cpp" {
		t.Fatalf("removed open lost: %v", changed)
	}
}

func TestP4SubmitRejectsRemovedInitialOpens(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_DEV\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n\tMultica: MUL-7\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/B.cpp\n... action edit\n... change 1001\n")
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV", User: "alice", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true}, change: "1001", ticket: "123", issueKey: "MUL-7", changeReported: true,
		initialOpened: map[string]p4OpenedFileSnapshot{"//depot/main/A.cpp": {action: "edit"}},
	}
	_, rej, err := (&Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}).runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root)
	if err != nil || rej == nil || rej.Code != p4CodeOpenedByYou {
		t.Fatalf("removed initial open did not stop submit: %+v %v", rej, err)
	}
	log, _ := os.ReadFile(logPath)
	for _, verb := range []string{"revert ", "shelve ", "submit "} {
		if strings.Contains(string(log), verb) {
			t.Fatalf("submit preflight changed files after removed opens: %s", log)
		}
	}
}

func TestP4ChangeRegisteredBeforeFirstWrite(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_CHANGEIN", "Change 1001 created.\n")
	for _, reuse := range []bool{false, true} {
		name := "create"
		if reuse {
			name = "reuse"
		}
		t.Run(name, func(t *testing.T) {
			var reports []p4ChangelistReport
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var report p4ChangelistReport
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
				}
				reports = append(reports, report)
				w.Write([]byte("{}"))
			}))
			defer api.Close()
			root := t.TempDir()
			b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
				ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main"}, taskID: "task", ticket: "123"}
			if reuse {
				b.change = "1001"
				b.descriptionReady = true
			}
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, client: NewClient(api.URL)}
			for range 2 {
				got, err := d.ensureTaskChange(t.Context(), b)
				if err != nil || got != "1001" {
					t.Fatalf("change=%q err=%v", got, err)
				}
			}
			if len(reports) != 1 || reports[0].Status != "pending" || reports[0].Number != 1001 || !reports[0].FencePolls {
				t.Fatalf("pending registration missing or repeated: %+v", reports)
			}
		})
	}
}

func TestP4AutoReviewRejectsProjectSwarmOrigin(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_TICKETS", "p4.example:1666 (alice) TEST-ONLY-P4-TICKET\n")
	var requests atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"reviews\":[{\"id\":42,\"changes\":[1001]}]}"))
	}))
	defer evil.Close()
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4.example:1666", User: "alice", Client: "alice_DEV"},
		ref: p4depot.Ref{Port: "p4.example:1666", Depot: "//depot/main", User: "alice", SwarmURL: evil.URL}, change: "1001", workspaceID: "ws"}
	trusted := b.ref
	trusted.SwarmURL = "https://trusted.example"
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(trusted)})
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(b.ref)})
	report := p4ChangelistReport{Number: 1001, Status: "shelved"}
	d.attachSwarmReview(t.Context(), b, &report)
	d.swarmReviewState(t.Context(), p4OpenChangelist{workspaceID: "ws", SwarmReviewNumber: 42}, b.ref)
	if requests.Load() != 0 {
		t.Fatalf("untrusted project origin received %d requests", requests.Load())
	}
	rec := httptest.NewRecorder()
	if _, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", b.ref); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("task origin overrode the admin catalog: status=%d", rec.Code)
	}
}

func TestP4RevertUnchangedSelectsOnlyRunOpens(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_DEPOT", "//depot/main")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //depot/main/B.cpp\n... clientFile "+filepath.Join(root, "B.cpp")+"\n... change 1001\n... action edit\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/A.cpp\n... change 1001\n... action edit\n\n... depotFile //depot/main/B.cpp\n... change 1001\n... action edit\n\n... depotFile //depot/main/C.cpp\n... change 1001\n... action edit\n")
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main"}, change: "1001", ticket: "123", descriptionReady: true, changeReported: true,
		initialOpened: map[string]p4OpenedFileSnapshot{"//depot/main/A.cpp": {path: filepath.Join(root, "A.cpp"), action: "edit"}},
	}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
	if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "edit", Files: []string{"B.cpp"}}, root); rej != nil || err != nil {
		t.Fatalf("edit: %+v %v", rej, err)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "revert", RevertUnchanged: true}, root); rej != nil || err != nil {
		t.Fatalf("revert: %+v %v", rej, err)
	}
	d.client = p4RegistrationTestClient(t)
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task"}, b); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(logPath)
	var commands int
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.Contains(line, "revert ") {
			continue
		}
		commands++
		if !strings.Contains(line, "B.cpp") || strings.Contains(line, "A.cpp") || strings.Contains(line, "C.cpp") || strings.Contains(line, "...") {
			t.Fatalf("cleanup escaped the run's files: %s", line)
		}
	}
	if commands != 2 {
		t.Fatalf("revert commands=%d\n%s", commands, log)
	}
}

func TestP4AutoReviewUsesAdminCatalogOrigin(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_TICKETS", "p4.example:1666 (alice) TEST-ONLY-P4-TICKET\n")
	var requests atomic.Int32
	trusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ticket, ok := r.BasicAuth()
		if !ok || user != "alice" || ticket != "TEST-ONLY-P4-TICKET" {
			t.Error("configured Swarm did not receive the test credential")
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/reviews/42") {
			w.Write([]byte(`{"review":{"id":42,"state":"needsReview"}}`))
			return
		}
		w.Write([]byte(`{"reviews":[{"id":42,"changes":[1001]}]}`))
	}))
	defer trusted.Close()
	ref := p4depot.Ref{Port: "p4.example:1666", Depot: "//depot/main", User: "alice", SwarmURL: trusted.URL}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(ref)})
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: ref.Port, User: ref.User}, ref: ref, workspaceID: "ws"}
	report := p4ChangelistReport{Number: 1001, Status: "shelved"}
	d.attachSwarmReview(t.Context(), b, &report)
	if requests.Load() != 1 || report.SwarmReview == nil || report.SwarmReview.Number != 42 {
		t.Fatalf("configured review failed: requests=%d report=%+v", requests.Load(), report)
	}
	if state := d.swarmReviewState(t.Context(), p4OpenChangelist{workspaceID: "ws", SwarmReviewNumber: 42}, ref); state != "needsReview" || requests.Load() != 2 {
		t.Fatalf("configured Swarm polling failed: state=%q requests=%d", state, requests.Load())
	}
}

func TestP4FinishReportsRemovedInitialOpens(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_OPENED", "")
	var got p4ChangelistReport
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte("{}"))
	}))
	defer api.Close()
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main"}, taskID: "task", change: "1001", changeReported: true,
		initialOpened: map[string]p4OpenedFileSnapshot{"//depot/main/A.cpp": {action: "edit"}},
	}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, client: NewClient(api.URL)}
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task"}, b); err == nil || !strings.Contains(err.Error(), "pre-existing opens were removed") {
		t.Fatalf("removed opens were silent: %v", err)
	}
	log, _ := os.ReadFile(logPath)
	if got.Status != "pending" || got.Number != 1001 || strings.Contains(string(log), "revert ") || strings.Contains(string(log), "shelve ") || strings.Contains(string(log), "change -d") {
		t.Fatalf("receipt=%+v\n%s", got, log)
	}
}

func TestP4PendingRegistrationSurvivesRestart(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CHANGEIN", "Change 1001 created.\n")
	t.Setenv("P4_FAKE_DEPOT", "//depot/main")
	cfg := Config{WorkspacesRoot: t.TempDir(), ServerBaseURL: "http://offline.invalid", DaemonID: "daemon"}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, client: NewClient(cfg.ServerBaseURL), terminalReports: newTerminalReportStore(cfg), logger: slog.Default()}
	d.terminalReportSend = func(context.Context, terminalTaskReport, []time.Duration) error { return errors.New("offline") }
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV", Root: root, Dir: root}, ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main"}, taskID: "task", ticket: "123"}
	if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "edit", Files: []string{"A.cpp"}}, root); err == nil || rej != nil {
		t.Fatalf("offline registration allowed edit: %+v %v", rej, err)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "edit -c") {
		t.Fatalf("file write preceded pending registration:\n%s", log)
	}
	for _, action := range []string{"change", "revert", "shelve", "submit", "unshelve"} {
		if _, _, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: action, RevertUnchanged: true}, root); err == nil {
			t.Fatalf("%s bypassed the failed pending registration", action)
		}
	}
	var replayed p4ChangelistReport
	restarted := &Daemon{client: NewClient(cfg.ServerBaseURL), terminalReports: newTerminalReportStore(cfg), logger: slog.Default()}
	restarted.terminalReportSend = func(_ context.Context, report terminalTaskReport, _ []time.Duration) error {
		return json.Unmarshal([]byte(report.output), &replayed)
	}
	if pending, delivered := restarted.replayPendingTerminalReports(t.Context()); pending != 0 || delivered != 1 || replayed.Status != "pending" || replayed.Number != 1001 {
		t.Fatalf("pending=%d delivered=%d receipt=%+v", pending, delivered, replayed)
	}
}
