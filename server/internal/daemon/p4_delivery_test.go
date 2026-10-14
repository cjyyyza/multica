package daemon

import (
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

func TestP4SubmitReportsAuthoritativeReceipt(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_main\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n")
	t.Setenv("P4_FAKE_MUTATE", "Change 1001 renamed change 2002 and submitted.\n")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 2002\n... oldChange 1001\n... status submitted\n... depotFile0 //depot/main/a.cpp\n... action0 edit\n... type0 text\n... depotFile1 //depot/main/b.cpp\n... action1 add\n... type1 text\n")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/a.cpp\n... change 1001\n... action edit\n")
	var got p4ChangelistReport
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	b := &p4TaskBinding{
		ws:     &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_main", Root: root, Dir: root, Stream: "//depot/main"},
		ref:    p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true},
		taskID: "task", ticket: "123", change: "1001",
	}
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, logger: slog.Default()}
	result, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root)
	if err != nil || rej != nil {
		t.Fatalf("submit rejected: %v %+v", err, rej)
	}
	if result.Changelist != "2002" {
		t.Errorf("CLI receipt uses %s instead of submitted number 2002", result.Changelist)
	}
	status, err := d.workspaceStatus(t.Context(), b)
	if err != nil || status.Change != "2002" || len(status.Files) != 2 {
		t.Errorf("submitted workspace status = %+v, %v", status, err)
	}
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task"}, b); err != nil {
		t.Fatal(err)
	}
	if got.Number != 2002 || got.OriginalNumber != 1001 || got.FilesCount != 2 || got.Status != "submitted" {
		t.Fatalf("submitted receipt = %+v", got)
	}
}

func TestP4SubmitReceiptFailureDoesNotResubmitOrReportCompletion(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_main\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n")
	t.Setenv("P4_FAKE_MUTATE", "Change 1001 submitted.\n")
	t.Setenv("P4_FAKE_DESCRIBE_ERR", "connection lost")
	t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/a.cpp\n... change 1001\n... action edit\n")
	var got p4ChangelistReport
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	b := &p4TaskBinding{
		ws:     &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_main", Root: root, Dir: root},
		ref:    p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true},
		taskID: "task", ticket: "123", change: "1001",
	}
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, logger: slog.Default()}
	if _, _, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root); err == nil {
		t.Error("missing submission receipt was reported as success")
	}
	if _, rej, _ := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root); rej == nil {
		t.Error("submit was replayed after a receipt-read failure")
	}
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task"}, b); err == nil {
		t.Error("missing submission receipt did not fail delivery")
	}
	if got.Number != 1001 || got.Status != "pending" {
		t.Errorf("unverified change must stay eligible for polling: %+v", got)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	afterSubmit := strings.SplitN(string(log), "submit -c 1001", 2)
	if len(afterSubmit) != 2 || strings.Contains(afterSubmit[1], "submit ") || strings.Contains(afterSubmit[1], "revert ") || strings.Contains(afterSubmit[1], "shelve ") {
		t.Fatalf("unexpected writes after submission: %s", log)
	}
}

func TestP4FinishFailureReportsPendingChange(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_MUTATE_ERR", "simulated connection failure")
	var got p4ChangelistReport
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	root := t.TempDir()
	b := &p4TaskBinding{
		ws:     &p4cache.Workspace{Port: "p4.example:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
		ref:    p4depot.Ref{Port: "p4.example:1666", Depot: "//depot/Dev"},
		taskID: "task", ticket: "123", change: "1001",
	}
	d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, logger: slog.Default()}
	d.rememberP4Binding(b)
	if err := d.finishP4Workspace(t.Context(), Task{ID: "task"}, b); err == nil {
		t.Fatal("failed Perforce finalization was reported as success")
	}
	if got.Number != 1001 || got.Status != "pending" {
		t.Fatalf("lost the failed run's changelist: %+v", got)
	}
	if d.p4Binding("task") != nil {
		t.Fatal("finished task still owns the workspace binding")
	}
}

func TestP4ReportSurvivesRestartAndDoesNotReplaceTaskResult(t *testing.T) {
	var online atomic.Bool
	var reports, failures atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer current-daemon-token" {
			t.Error("replay did not use the daemon's current credential")
		}
		if strings.HasSuffix(r.URL.Path, "/p4/changelists") {
			var got p4ChangelistReport
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.Number != 1001 || got.Status != "shelved" {
				t.Errorf("lost the exact changelist report: %+v, %v", got, err)
			}
			reports.Add(1)
		} else {
			failures.Add(1)
		}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	cfg := Config{WorkspacesRoot: t.TempDir(), ServerBaseURL: api.URL, DaemonID: "daemon"}
	d := &Daemon{client: NewClient(api.URL), terminalReports: newTerminalReportStore(cfg), logger: slog.Default()}
	if err := d.reportTaskP4Changelist(t.Context(), Task{ID: "task"}, p4ChangelistReport{Port: "p4.example:1666", Number: 1001, OriginalNumber: 1001, Status: "shelved"}); err == nil {
		t.Fatal("503 was acknowledged")
	}
	if err := d.terminalReports.enqueue(terminalTaskReport{kind: terminalTaskReportFail, taskID: "task", errorMessage: "delivery failed"}); err != nil {
		t.Fatalf("P4 receipt conflicts with task result: %v", err)
	}
	// Reopen the same namespace without retaining any in-memory task binding.
	online.Store(true)
	client := NewClient(api.URL)
	client.SetToken("current-daemon-token")
	restarted := &Daemon{client: client, terminalReports: newTerminalReportStore(cfg), logger: slog.Default()}
	if pending, delivered := restarted.replayPendingTerminalReports(t.Context()); pending != 0 || delivered != 2 {
		t.Fatalf("replay: pending=%d delivered=%d", pending, delivered)
	}
	if reports.Load() != 1 || failures.Load() != 1 {
		t.Fatalf("reports=%d failures=%d", reports.Load(), failures.Load())
	}
	if records, err := restarted.terminalReports.list(); err != nil || len(records) != 0 {
		t.Fatalf("acknowledged reports remain in %s: %+v %v", filepath.Base(restarted.terminalReports.dir), records, err)
	}
}
