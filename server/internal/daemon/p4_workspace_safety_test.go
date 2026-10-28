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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestReusedP4ChangeDescriptionKeepsHumanContentAndRejectsConflicts(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	const original = "Human summary\n\n#review-42\nMultica: MUL-7"
	for _, tc := range []struct {
		name, before, current string
		allowed, repaired     bool
	}{
		{name: "missing ticket", before: original, current: original, allowed: true, repaired: true},
		{name: "valid ticket", before: "refs #123\n" + original, current: "refs #123\n" + original, allowed: true},
		{name: "foreign ticket", before: "refs #456\n" + original, current: "refs #456\n" + original},
		{name: "foreign issue", before: original, current: "refs #123\nMultica: MUL-8"},
		{name: "ticket removed after binding", before: "refs #123\n" + original, current: original},
		{name: "malformed ticket", before: "refs #TODO\n" + original, current: "refs #TODO\n" + original},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			logPath, specPath := filepath.Join(root, "p4.log"), filepath.Join(root, "spec.txt")
			t.Setenv("P4_FAKE_LOG", logPath)
			t.Setenv("P4_FAKE_SPEC", specPath)
			t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_DEV\nUser: alice\nStatus: pending\nDescription:\n\t"+strings.ReplaceAll(tc.current, "\n", "\n\t")+"\n")
			b := &p4TaskBinding{
				ws:     &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
				ref:    p4depot.Ref{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"},
				change: "1001", ticket: "123", issueKey: "MUL-7", description: tc.before,
				initialOpened:  map[string]p4OpenedFileSnapshot{},
				changeReported: true,
			}
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
			_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "edit", Files: []string{"A.cpp"}}, root)
			if tc.allowed != (err == nil && rej == nil) {
				t.Fatalf("rejection=%+v error=%v", rej, err)
			}
			log, _ := os.ReadFile(logPath)
			if !tc.allowed {
				if strings.Contains(string(log), "change -i") || strings.Contains(string(log), "edit -c") || b.changeTouched {
					t.Fatalf("conflicting description was changed:\n%s", log)
				}
			}
			spec, _ := os.ReadFile(specPath)
			if tc.repaired {
				if !strings.Contains(string(spec), "\trefs #123\n") || !strings.Contains(string(spec), "\tHuman summary\n\t\n\t#review-42\n\tMultica: MUL-7") {
					t.Fatalf("repair lost human text or review markers:\n%s", spec)
				}
			} else if len(spec) != 0 {
				t.Fatalf("description was unnecessarily rewritten:\n%s", spec)
			}
		})
	}
}

func TestP4ClientWaitRenewsLeaseAndCancels(t *testing.T) {
	var waiting, leases atomic.Int32
	ready := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wait-local-directory"):
			waiting.Add(1)
			select {
			case ready <- struct{}{}:
			default:
			}
			w.Write([]byte("{}"))
		case strings.HasSuffix(r.URL.Path, "/prepare-lease"):
			leases.Add(1)
			w.Write([]byte("{}"))
		case strings.HasSuffix(r.URL.Path, "/status"):
			if leases.Load() > 0 {
				w.Write([]byte(`{"status":"cancelled"}`))
			} else {
				w.Write([]byte(`{"status":"dispatched"}`))
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer api.Close()
	d := &Daemon{p4ClientLocks: NewLocalPathLocker(), client: NewClient(api.URL), cancelPollInterval: 5 * time.Millisecond, prepareLeaseRefresh: 5 * time.Millisecond}
	ws := &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV"}
	release, err := d.p4ClientLocks.Acquire(t.Context(), "p4:1666|alice_DEV", "holder", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		release, err := d.acquireP4ClientLock(ctx, Task{ID: "waiter", RuntimeID: "runtime"}, ws)
		if release != nil {
			release()
		}
		result <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("wait status not reported")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || leases.Load() == 0 || waiting.Load() != 1 || d.resourceWaitTasks.Load() != 0 {
			t.Fatalf("err=%v waits=%d leases=%d queued=%d", err, waiting.Load(), leases.Load(), d.resourceWaitTasks.Load())
		}
	case <-ctx.Done():
		t.Fatal("cancelled waiter retained its slot")
	}
	if d.p4ClientLocks.Holder("p4:1666|alice_DEV") != "holder" {
		t.Fatal("waiter disturbed the holder")
	}
}

func TestP4ClientWaitingDoesNotConsumePreparationDeadline(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+root+"\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+root+"\n... Stream //depot/main\n")
	waiting := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait-local-directory") {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
		if strings.HasSuffix(r.URL.Path, "/status") {
			w.Write([]byte(`{"status":"dispatched"}`))
		} else {
			w.Write([]byte("{}"))
		}
	}))
	defer api.Close()
	d := &Daemon{
		cfg: Config{DaemonID: "daemon"}, logger: slog.Default(),
		p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker(), client: NewClient(api.URL),
		taskPrepareTimeout: 20 * time.Millisecond,
	}
	release, err := d.p4ClientLocks.Acquire(t.Context(), "p4:1666|alice_DEV", "holder", nil)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()
	raw, _ := json.Marshal(localDirectoryRef{LocalPath: root, DaemonID: "daemon", ExecutionMode: localDirectoryModeInPlace})
	task := Task{
		ID: "waiter", IssueID: "issue", WorkspaceID: "ws", AgentID: "agent", Agent: &AgentData{ID: "agent"},
		P4Depots:         []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}},
		ProjectResources: []ProjectResourceData{{ResourceType: "local_directory", ResourceRef: raw}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := d.runTask(ctx, task, "codex", 0, slog.Default()); done <- err }()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("P4 contention did not report waiting")
	}
	timer := time.NewTimer(4 * d.taskPrepareTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("P4 wait used preparation deadline: %v", err)
	case <-timer.C:
	}
	unlock()
	select {
	case err := <-done:
		if errors.Is(err, errTaskPrepareTimeout) || err == nil || !strings.Contains(err.Error(), "codex") {
			t.Fatalf("expected runtime lookup only after releasing P4 client, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("P4 waiter did not resume")
	}
}

func TestWorkspaceOpenedWithoutTaskChangeIsEmpty(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/Other.cpp\n... change 999\n... action edit\n")
	b := &p4TaskBinding{ws: &p4cache.Workspace{Client: "alice_DEV"}}
	result, rej, err := (&Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}).runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "opened"}, "")
	if err != nil || rej != nil || result.Output != "" || result.Changelist != "" {
		t.Fatalf("unbound opened: result=%+v rejection=%+v error=%v", result, rej, err)
	}
}

func TestWorkspaceRevertUnchangedWithoutRunOpensDoesNothing(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", Client: "alice_DEV", Root: root, Dir: root}, change: "1001"}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
	_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "revert", RevertUnchanged: true}, root)
	log, _ := os.ReadFile(logPath)
	if err != nil || rej != nil || strings.Contains(string(log), "revert ") {
		t.Fatalf("revert: rejection=%+v error=%v\n%s", rej, err, log)
	}
}

func TestP4SubmitRejectsShelfOnlyFiles(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_DEV\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n\tMultica: MUL-7\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/B.cpp\n... change 1001\n... action edit\n")
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... depotFile0 //depot/main/A.cpp\n... action0 edit\n")
	b := &p4TaskBinding{
		ws:  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true}, change: "1001", ticket: "123", issueKey: "MUL-7",
	}
	_, rej, err := (&Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}).runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root)
	log, _ := os.ReadFile(logPath)
	if err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
		t.Fatalf("submit with shelf-only work: rejection=%+v error=%v", rej, err)
	}
	if strings.Contains(string(log), "shelve -d") || strings.Contains(string(log), "submit -c") {
		t.Fatalf("submit deleted shelf-only work:\n%s", log)
	}
}

func TestReusedP4ChangeFinishDetectsDirectFileEdits(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, tc := range []struct {
		name                          string
		edit, retitle, partialFailure bool
	}{
		{name: "read only"},
		{name: "direct edit", edit: true},
		{name: "metadata only", retitle: true},
		{name: "partial write failure", edit: true, partialFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "A.cpp")
			if err := os.WriteFile(file, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "p4.log")
			t.Setenv("P4_FAKE_LOG", logPath)
			t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\nP4USER=alice\n")
			t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+root+"\n")
			t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+root+"\n... Stream //depot/main\n")
			t.Setenv("P4_FAKE_CHANGES", "Change 1001 on 2026/10/27 by alice@alice_DEV *pending*\n\n\trefs #123 fix\n\n\tMultica: MUL-7\n")
			t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_DEV\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n\tMultica: MUL-7\n")
			t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/A.cpp\n... action edit\n... change 1001\n")
			t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... depotFile0 //depot/main/A.cpp\n... action0 edit\n... type0 text\n")
			t.Setenv("P4_FAKE_WHERE", "... depotFile //depot/main/A.cpp\n... path "+file+"\n")
			var reports int
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reports++
				var report p4ChangelistReport
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
				}
				wantStatus := "shelved"
				if reports == 1 {
					wantStatus = "pending"
				}
				if report.Status != wantStatus {
					t.Errorf("report=%+v", report)
				}
				w.Write([]byte("{}"))
			}))
			defer api.Close()
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker(), client: NewClient(api.URL), logger: slog.Default()}
			task := Task{ID: "task", IssueID: "issue", IssueIdentifier: "MUL-7", P4Ticket: "123", P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main", NoAutoReview: true}}}
			b, err := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: root, RealPath: root}, "codex")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("P4_FAKE_FSTAT", "")
			if tc.retitle {
				_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "change", Description: "refs #123 updated title\n\nMultica: MUL-7"}, root)
				if err != nil || rej != nil {
					t.Fatalf("retitle: %+v %v", rej, err)
				}
			}
			if tc.partialFailure {
				t.Setenv("P4_FAKE_MUTATE_ERR", "connection lost after partial edit")
				_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "edit", Files: []string{"A.cpp"}}, root)
				if err == nil || rej != nil {
					t.Fatalf("partial edit: %+v %v", rej, err)
				}
				t.Setenv("P4_FAKE_MUTATE_ERR", "")
			}
			if tc.edit {
				if err := os.WriteFile(file, []byte("new\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.finishP4Workspace(t.Context(), task, b); err != nil {
				t.Fatal(err)
			}
			log, _ := os.ReadFile(logPath)
			wantReports := 2
			if strings.Contains(string(log), "revert ") {
				t.Fatalf("close-out reverted a pre-existing human open:\n%s", log)
			}
			if reports != wantReports || (!tc.edit && (strings.Contains(string(log), "revert ") || strings.Contains(string(log), "shelve -f"))) {
				t.Fatalf("reports=%d, want %d; read-only runs must not mutate:\n%s", reports, wantReports, log)
			}
			if d.p4Binding(task.ID) != nil || d.p4ClientLocks.Holder("p4:1666|alice_DEV") != "" {
				t.Fatal("finish retained the binding or client lock")
			}
		})
	}
}

func TestCoordinationP4BindingDoesNotWaitOrWrite(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+root+"\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+root+"\n... Stream //depot/main\n")
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker()}
	release, err := d.p4ClientLocks.Acquire(t.Context(), "p4:1666|alice_DEV", "writer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, chat := range []bool{false, true} {
		task := Task{ID: "reader", P4Ticket: "123", P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true, AllowSync: true, AllowUnshelve: true}}}
		if chat {
			task.ChatSessionID = "chat"
			task.IssueID = "issue"
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		b, err := d.bindP4Workspace(ctx, task, &localDirectoryAssignment{AbsPath: root, RealPath: root}, "codex")
		cancel()
		if err != nil || b == nil {
			t.Fatalf("coordination bind waited for writer: %v", err)
		}
		for _, action := range []string{"edit", "add", "delete", "move", "reopen", "reconcile", "change", "revert", "shelve", "submit", "sync", "unshelve"} {
			_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: action, Files: []string{"A.cpp"}}, root)
			if err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
				t.Fatalf("%s reached writable P4: %+v %v", action, rej, err)
			}
		}
		if err := d.finishP4Workspace(t.Context(), task, b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestP4FallbackSkipsOnlyConfirmedNonWorkspaces(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, tc := range []struct {
		name                                                                           string
		explicit, missing, hiddenRoot, outside, missingRoot, authFailure, foreignOwner bool
		wantBinding, wantError                                                         bool
	}{
		{name: "fallback without a client", missing: true},
		{name: "explicit project without a client", explicit: true, missing: true, wantError: true},
		{name: "fallback outside client root", outside: true},
		{name: "explicit project outside client root", explicit: true, outside: true, wantError: true},
		{name: "fallback authentication failure", authFailure: true, wantError: true},
		{name: "fallback unverified client root", hiddenRoot: true, wantError: true},
		{name: "fallback missing client root on disk", missingRoot: true, wantError: true},
		{name: "fallback foreign owner", foreignOwner: true, wantError: true},
		{name: "fallback valid workspace", wantBinding: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			clientRoot := root
			if tc.outside {
				clientRoot = t.TempDir()
			}
			if tc.missingRoot {
				clientRoot = filepath.Join(root, "missing-root")
			}
			t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\nP4CLIENT=alice_DEV\n")
			info := "... userName alice\n... clientName alice_DEV\n... clientRoot " + clientRoot + "\n"
			if tc.missing {
				info = "... userName alice\n... clientName unknown\n"
			}
			if tc.hiddenRoot {
				info = "... userName alice\n... clientName alice_DEV\n"
			}
			t.Setenv("P4_FAKE_INFO", info)
			owner := "alice"
			if tc.foreignOwner {
				owner = "bob"
			}
			t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner "+owner+"\n... Root "+clientRoot+"\n... Stream //depot/main\n")
			if tc.authFailure {
				t.Setenv("P4_FAKE_INFO_ERR", "Perforce password invalid or unset.")
			}
			task := Task{ID: "task", IssueID: "issue", P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}}}
			if tc.explicit {
				task.ProjectResources = []ProjectResourceData{{ResourceType: "perforce_depot"}}
			}
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker()}
			b, err := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: root, RealPath: root}, "codex")
			if b != nil {
				t.Cleanup(b.release)
			}
			if (err != nil) != tc.wantError || (b != nil) != tc.wantBinding {
				t.Fatalf("binding=%+v error=%v", b, err)
			}
		})
	}
}
