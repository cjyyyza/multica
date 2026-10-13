package daemon

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

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
