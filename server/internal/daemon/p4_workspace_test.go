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
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestWorkspaceRunInvariants(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	if abs, absErr := filepath.Abs(root); absErr == nil {
		if resolved, resolveErr := util.ResolveSymlinks(abs); resolveErr == nil {
			root = resolved
		}
	}
	depotFile := "//DJ01_Proj/Dev/Game/A.cpp"

	cases := []struct {
		name       string
		action     string
		files      []string
		changelist string
		ticket     string
		change     string
		fstat      string
		snapshot   []string
		allow      bool
		edge       bool
		sidecars   []string
		unchanged  bool
		omitDepot  bool
		wantCode   string
		wantHolder string
		wantLog    string
		forbidLog  string
		wantWarn   string
	}{
		{
			name:      "I7 ticket required",
			action:    "edit",
			files:     []string{"Game/A.cpp"},
			ticket:    "",
			wantCode:  p4CodeTicketRequired,
			forbidLog: "change -i",
		},
		{
			name:       "I1 foreign changelist",
			action:     "edit",
			files:      []string{"Game/A.cpp"},
			changelist: "999",
			change:     "1001",
			wantCode:   p4CodeOutsideTaskChange,
		},
		{
			name:     "I2 already in a numbered changelist",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			fstat:    "... depotFile " + depotFile + "\n... change 50\n... action edit\n",
			wantCode: p4CodeOpenedByYou,
		},
		{
			name:     "I2 already in the default changelist",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			fstat:    "... depotFile " + depotFile + "\n... change default\n... action edit\n",
			wantCode: p4CodeOpenedByYou,
		},
		{
			name:       "I3 exclusive lock",
			action:     "edit",
			files:      []string{"Game/A.cpp"},
			change:     "1001",
			fstat:      "... depotFile " + depotFile + "\n... headType binary+l\n... otherOpen0 bob@bob_DEV\n... otherLock\n",
			wantCode:   p4CodeExclusiveLocked,
			wantHolder: "bob@bob_DEV",
		},
		{
			name:     "I3 exclusive type not allowed",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			fstat:    "... depotFile " + depotFile + "\n... headType binary+l\n... headRev 3\n... haveRev 3\n",
			wantCode: p4CodeExclusiveNotAllowed,
		},
		{
			name:     "I3 exclusive type allowed warns",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			allow:    true,
			fstat:    "... depotFile " + depotFile + "\n... headType binary+l\n... headRev 3\n... haveRev 3\n",
			wantLog:  "edit -c 1001",
			wantWarn: "exclusive_held",
		},
		{
			name:     "I4 reconcile without paths",
			action:   "reconcile",
			change:   "1001",
			wantCode: p4CodeActionNotAllowed,
		},
		{
			name:     "I4 revert without paths",
			action:   "revert",
			change:   "1001",
			wantCode: p4CodeActionNotAllowed,
		},
		{
			name:     "I5 unshelve",
			action:   "unshelve",
			change:   "1001",
			wantCode: p4CodeActionNotAllowed,
		},
		{
			name:     "I5 submit",
			action:   "submit",
			change:   "1001",
			wantCode: p4CodeActionNotAllowed,
		},
		{
			name:     "I5 sync",
			action:   "sync",
			wantCode: p4CodeActionNotAllowed,
		},
		{
			name:      "I6 sidecar",
			action:    "add",
			files:     []string{"AGENTS.md"},
			change:    "1001",
			sidecars:  []string{filepath.Join(root, "AGENTS.md")},
			wantCode:  p4CodeSidecarPath,
			forbidLog: " add ",
		},
		{
			name:     "I8 path outside the client",
			action:   "edit",
			files:    []string{filepath.Join(filepath.Dir(root), "outside.cpp")},
			change:   "1001",
			wantCode: p4CodeNotInWorkspace,
		},
		{
			name:      "edit creates the task changelist",
			action:    "edit",
			files:     []string{"Game/A.cpp"},
			omitDepot: true,
			wantLog:   "edit -c 1001",
		},
		{
			name:     "not at head warns",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			fstat:    "... depotFile " + depotFile + "\n... headType text\n... headRev 4\n... haveRev 2\n",
			wantLog:  "edit -c 1001",
			wantWarn: "not_head",
		},
		{
			name:     "opened elsewhere warns",
			action:   "edit",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			fstat:    "... depotFile " + depotFile + "\n... headType text\n... headRev 2\n... haveRev 2\n... otherOpen0 bob@bob_DEV\n",
			wantLog:  "edit -c 1001",
			wantWarn: "opened_elsewhere",
		},
		{
			name:     "reopen refuses the pre-task default changelist",
			action:   "reopen",
			files:    []string{"Game/A.cpp"},
			change:   "1001",
			snapshot: []string{depotFile},
			fstat:    "... depotFile " + depotFile + "\n... change default\n... action edit\n",
			wantCode: p4CodeOpenedByYou,
		},
		{
			name:    "reopen adopts a file the editor opened during the task",
			action:  "reopen",
			files:   []string{"Game/A.cpp"},
			change:  "1001",
			fstat:   "... depotFile " + depotFile + "\n... change default\n... action edit\n... headType text\n... headRev 1\n... haveRev 1\n",
			wantLog: "reopen -c 1001",
		},
		{
			name:    "edge shelve promotes",
			action:  "shelve",
			change:  "1001",
			edge:    true,
			wantLog: "shelve -f -p -c 1001",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "p4.log")
			t.Setenv("P4_FAKE_LOG", logPath)
			t.Setenv("P4_FAKE_FSTAT", tc.fstat)
			t.Setenv("P4_FAKE_MUTATE", "ok\n")
			t.Setenv("P4_FAKE_CHANGEIN", "Change 1001 created.\n")
			t.Setenv("P4USER", "alice")

			ws := &p4cache.Workspace{
				Port: "ssl:p4.example:1666", User: "alice", Client: "alice_DEV",
				Charset: "utf8", Root: root, Dir: root, Stream: "//DJ01_Proj/Dev", Edge: tc.edge,
			}
			binding := &p4TaskBinding{
				ws: ws,
				ref: p4depot.Ref{
					Port: ws.Port, Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
					AllowExclusive: tc.allow,
				},
				ticket:          "5002",
				issueKey:        "DJ-123",
				title:           "fix the crash",
				change:          tc.change,
				defaultSnapshot: tc.snapshot,
				sidecars:        tc.sidecars,
				taskID:          "task",
			}
			if tc.ticket == "" && tc.name == "I7 ticket required" {
				binding.ticket = ""
			}
			d := &Daemon{
				logger:     slog.Default(),
				p4Cache:    &p4cache.Cache{P4Path: bin},
				workspaces: map[string]*workspaceState{"ws": {}},
			}
			d.rememberP4Binding(binding)
			d.registerActiveRepoCheckoutTask("mat_p4_ws", activeRepoCheckoutTask{
				WorkspaceID: "ws", TaskID: "task", WorkDir: root,
			})

			body := map[string]any{
				"workspace_id": "ws",
				"task_id":      "task",
				"workdir":      root,
				"action":       tc.action,
				"files":        tc.files,
			}
			if !tc.omitDepot {
				body["port"] = ws.Port
				body["depot"] = "//DJ01_Proj/Dev"
			}
			if tc.changelist != "" {
				body["changelist"] = tc.changelist
			}
			if tc.unchanged {
				body["revert_unchanged"] = true
			}
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/p4/run", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer mat_p4_ws")
			rec := httptest.NewRecorder()
			d.p4RunHandler()(rec, req)

			logBody, _ := os.ReadFile(logPath)
			log := string(logBody)
			if tc.forbidLog != "" && strings.Contains(log, tc.forbidLog) {
				t.Fatalf("p4 log contains %q:\n%s", tc.forbidLog, log)
			}
			if tc.wantCode != "" {
				if rec.Code != http.StatusConflict {
					t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
				}
				var rej p4Rejection
				if err := json.Unmarshal(rec.Body.Bytes(), &rej); err != nil {
					t.Fatal(err)
				}
				if rej.Code != tc.wantCode {
					t.Fatalf("code = %q, want %s (%s)", rej.Code, tc.wantCode, rej.Message)
				}
				if tc.wantHolder != "" && !strings.Contains(strings.Join(rej.Holders, " "), tc.wantHolder) {
					t.Fatalf("holders = %#v", rej.Holders)
				}
				if tc.wantCode == p4CodeActionNotAllowed && tc.action == "submit" && !strings.Contains(rej.Message, "do not submit") {
					t.Fatalf("submit rejection = %q", rej.Message)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if tc.wantLog != "" && !strings.Contains(log, tc.wantLog) {
				t.Fatalf("log missing %q:\n%s", tc.wantLog, log)
			}
			if tc.wantWarn != "" && !strings.Contains(rec.Body.String(), tc.wantWarn) {
				t.Fatalf("response missing warning %s: %s", tc.wantWarn, rec.Body.String())
			}
			if tc.name == "edit creates the task changelist" && !strings.Contains(log, "change -i") {
				t.Fatalf("the task changelist was not created:\n%s", log)
			}
		})
	}
}

func TestWorkspaceSyncAndStatus(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_OPENED", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... action edit\n... change 1001\n... type text\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Game/A.cpp\n... headType text\n... headRev 2\n... haveRev 1\n")
	t.Setenv("P4USER", "alice")

	ws := &p4cache.Workspace{
		Port: "ssl:p4.example:1666", User: "alice", Client: "alice_DEV",
		Root: root, Dir: root, Stream: "//DJ01_Proj/Dev",
	}
	binding := &p4TaskBinding{
		ws: ws, ref: p4depot.Ref{Port: ws.Port, Depot: "//DJ01_Proj/Dev"},
		ticket: "5002", change: "1001", taskID: "task",
	}
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}
	d.rememberP4Binding(binding)
	d.registerActiveRepoCheckoutTask("mat_p4_ws", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})

	syncReq := httptest.NewRequest(http.MethodPost, "/p4/sync", strings.NewReader(`{"workspace_id":"ws","task_id":"task","workdir":"`+jsonEscape(root)+`"}`))
	syncReq.Header.Set("Authorization", "Bearer mat_p4_ws")
	syncRec := httptest.NewRecorder()
	d.p4SyncHandler()(syncRec, syncReq)
	if syncRec.Code != http.StatusConflict || !strings.Contains(syncRec.Body.String(), p4CodeActionNotAllowed) {
		t.Fatalf("sync while bound: %d %s", syncRec.Code, syncRec.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/p4/status", nil)
	statusReq.Header.Set("Authorization", "Bearer mat_p4_ws")
	statusRec := httptest.NewRecorder()
	d.p4StatusHandler()(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", statusRec.Code, statusRec.Body.String())
	}
	var status p4StatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Client != "alice_DEV" || status.Change != "1001" || status.Ticket != "5002" || len(status.Files) != 1 {
		t.Fatalf("status = %+v", status)
	}
	if status.Files[0].DepotFile != "//DJ01_Proj/Dev/Game/A.cpp" {
		t.Fatalf("files = %+v", status.Files)
	}

	d.forgetP4Binding("task")
	missing := httptest.NewRecorder()
	d.p4StatusHandler()(missing, statusReq)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "no_workspace") {
		t.Fatalf("unbound status: %d %s", missing.Code, missing.Body.String())
	}
}

func jsonEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}

func TestBindRejectsDepotTrackedSidecar(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_FAKE_LOG", filepath.Join(t.TempDir(), "p4.log"))
	t.Setenv("P4_FAKE_SET", "P4PORT=ssl:p4.example:1666\nP4USER=alice\nP4CHARSET=utf8\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+abs+"\n... serverServices commit-server\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+abs+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/AGENTS.md\n... headType text\n")
	t.Setenv("P4USER", "alice")

	d := &Daemon{
		logger:        slog.Default(),
		p4Cache:       &p4cache.Cache{P4Path: bin},
		p4ClientLocks: NewLocalPathLocker(),
	}
	task := Task{
		ID:              "task",
		P4Ticket:        "5002",
		IssueIdentifier: "DJ-123",
		ThreadName:      "fix",
		P4Depots: []P4DepotData{{
			Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
		}},
	}
	_, bindErr := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: abs, RealPath: abs}, "codex")
	if bindErr == nil || !strings.Contains(bindErr.Error(), "point the local directory resource at the workspace root") {
		t.Fatalf("error = %v", bindErr)
	}
}

// A real server answers "no such file(s)" for a bare directory even when it
// tracks files under it (DJ01's Game/DJ01/.claude/skills), so a tracked skills
// directory is only found through <dir>/....
func TestBindRejectsSidecarDirectoryTrackedOnlyUnderneath(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_FAKE_LOG", filepath.Join(t.TempDir(), "p4.log"))
	t.Setenv("P4_FAKE_SET", "P4PORT=ssl:p4.example:1666\nP4USER=alice\nP4CHARSET=utf8\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+abs+"\n... serverServices commit-server\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+abs+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT_WILDCARD", "... depotFile //DJ01_Proj/Dev/Game/DJ01/.claude/skills/edit-flow/SKILL.md\n... headType text\n")
	t.Setenv("P4USER", "alice")

	d := &Daemon{
		logger:        slog.Default(),
		p4Cache:       &p4cache.Cache{P4Path: bin},
		p4ClientLocks: NewLocalPathLocker(),
	}
	task := Task{
		ID:       "task",
		P4Ticket: "5002",
		P4Depots: []P4DepotData{{
			Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
		}},
	}
	_, bindErr := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: abs, RealPath: abs}, "claude")
	if bindErr == nil || !strings.Contains(bindErr.Error(), ".claude/skills/edit-flow/SKILL.md") {
		t.Fatalf("error = %v, want the tracked skill file named", bindErr)
	}
}

func TestBindReusesTheIssueChangelist(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_FAKE_LOG", filepath.Join(t.TempDir(), "p4.log"))
	t.Setenv("P4_FAKE_SET", "P4PORT=ssl:p4.example:1666\nP4USER=alice\nP4CHARSET=utf8\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+abs+"\n... serverServices edge-server\n... serverCluster p4_dj01\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+abs+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT_ERR", "no such file(s).")
	t.Setenv("P4_FAKE_OPENED", "")
	t.Setenv("P4_FAKE_CHANGES", "Change 1001 on 2026/10/12 by alice@alice_DEV *pending*\n\n\trefs #5002 fix\n\n\tMultica: DJ-123\n")
	t.Setenv("P4USER", "alice")

	d := &Daemon{
		logger:        slog.Default(),
		p4Cache:       &p4cache.Cache{P4Path: bin},
		p4ClientLocks: NewLocalPathLocker(),
	}
	task := Task{
		ID: "task", P4Ticket: "5002", IssueIdentifier: "DJ-123", ThreadName: "fix",
		P4Depots: []P4DepotData{{
			Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
		}},
	}
	binding, bindErr := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: abs, RealPath: abs}, "codex")
	if bindErr != nil {
		t.Fatalf("bind: %v", bindErr)
	}
	t.Cleanup(binding.release)
	spec := binding.envSpec()
	if spec.Change != "1001" || spec.Client != "alice_DEV" || spec.Ticket != "5002" || !binding.ws.Edge {
		t.Fatalf("spec = %+v edge=%v", spec, binding.ws.Edge)
	}
	if spec.Stream != "//DJ01_Proj/Dev" {
		t.Fatalf("stream = %q", spec.Stream)
	}
}

func TestBindFailsWhenTheClientIsNotThisMachine(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_FAKE_SET", "P4PORT=ssl:p4.example:1666\nP4USER=alice\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+abs+"\n... serverServices commit-server\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Host OTHER-MACHINE\n... Root "+abs+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4USER", "alice")
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker()}
	task := Task{
		ID: "task",
		P4Depots: []P4DepotData{{
			Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
		}},
	}
	_, bindErr := d.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: abs, RealPath: abs}, "codex")
	if bindErr == nil || !strings.Contains(bindErr.Error(), "restricted to host") {
		t.Fatalf("error = %v", bindErr)
	}
}

func TestMatchTaskDepotPrefersTheLongestCoveringPath(t *testing.T) {
	ws := &p4cache.Workspace{Port: "ssl:p4.example:1666", Client: "alice_DEV", Stream: "//DJ01_Proj/Dev"}
	ref, err := matchTaskDepot([]P4DepotData{
		{Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj", Stream: "//DJ01_Proj"},
		{Port: "OTHER:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev"},
		{Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev"},
	}, ws)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Depot != "//DJ01_Proj/Dev" || ref.Stream != "//DJ01_Proj/Dev" {
		t.Fatalf("ref = %+v", ref)
	}

	classic := &p4cache.Workspace{
		Port: "ssl:p4.example:1666", Client: "alice_DEV",
		View: []string{"//DJ01_Proj/Dev/... //alice_DEV/Dev/..."},
	}
	ref, err = matchTaskDepot([]P4DepotData{{
		Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev",
	}}, classic)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Depot != "//DJ01_Proj/Dev" {
		t.Fatalf("classic ref = %+v", ref)
	}

	_, err = matchTaskDepot([]P4DepotData{{
		Port: "ssl:p4.example:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev",
	}}, &p4cache.Workspace{Port: "ssl:p4.example:1666", Client: "alice_DEV", Stream: "//Other/Main"})
	if err == nil || !strings.Contains(err.Error(), "not under a depot") {
		t.Fatalf("error = %v", err)
	}
}
