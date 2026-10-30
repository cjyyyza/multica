package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestWorkspaceFilelogAndDescribeStayInsideAuthorizedDepots(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FILELOG", "//DJ01_Proj/Dev/Foo.gbd\n... #3 change 12345 edit on 2024/01/15 12:00:00 by alice@alice_DEV (binary) '修正 GBD'\n\n\t修正 GBD\n\t先例\n")
	t.Setenv("P4_FAKE_DESCRIBE", "Change 12345 by alice@alice_DEV on 2024/01/15 12:00:00\n\n\t修正 GBD\n\nAffected files ...\n\n... //DJ01_Proj/Dev/Foo.gbd#3 edit\n")
	ws := &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root, Stream: "//DJ01_Proj/Dev"}
	binding := &p4TaskBinding{
		ws: ws, ticket: "5002", change: "1001", taskID: "task", readOnly: true,
		ref: p4depot.Ref{Port: ws.Port, Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev"},
	}
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}

	result, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "filelog", Files: []string{"Foo.gbd"}}, root)
	if err != nil || rej != nil {
		t.Fatalf("read-only filelog: err=%v rej=%+v", err, rej)
	}
	if len(result.Revisions) != 1 || result.Revisions[0].Change != "12345" || !strings.Contains(result.Revisions[0].Description, "先例") {
		t.Fatalf("revisions = %+v", result.Revisions)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "edit", Files: []string{"Foo.gbd"}}, root); err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
		t.Fatalf("read-only edit: err=%v rej=%+v", err, rej)
	}

	described, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "describe", Changelist: "12345"}, root)
	if err != nil || rej != nil {
		t.Fatalf("describe history: err=%v rej=%+v", err, rej)
	}
	if described.Description != "修正 GBD" || described.Changelist != "12345" || len(described.Files) != 1 {
		t.Fatalf("describe = %+v files=%+v", described.MutateResult, described.Files)
	}

	t.Setenv("P4_FAKE_DESCRIBE", "Change 12345 by alice@alice_DEV on 2024/01/15 12:00:00\n\n\tsecret\n\nAffected files ...\n\n... //secret/Foo.gbd#3 edit\n")
	_, foreign, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "describe", Changelist: "12345"}, root)
	if err != nil || foreign == nil || foreign.Code != p4CodeNotInWorkspace {
		t.Fatalf("foreign depot describe: err=%v rej=%+v", err, foreign)
	}
	if strings.Contains(strings.Join(foreign.Files, " "), "//secret") {
		t.Fatalf("rejection leaked the other depot: %+v", foreign)
	}
	t.Setenv("P4_FAKE_DESCRIBE", "Change 12345 by alice@alice_DEV on 2024/01/15 12:00:00\n\n\tmixed scope\n\nAffected files ...\n\n... //DJ01_Proj/Dev/Foo.gbd#3 edit\n... //secret/Confidential Plan.txt#1 edit\n")
	if result, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "describe", Changelist: "12345"}, root); err != nil || result != nil || rej == nil || rej.Code != p4CodeNotInWorkspace {
		t.Fatalf("mixed changelist with spaces escaped authorization: result=%+v rejection=%+v error=%v", result, rej, err)
	}
	t.Setenv("P4_FAKE_DESCRIBE", "... change 999\n... status submitted\n")
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "describe", Changelist: "999"}, root); err != nil || rej == nil {
		t.Fatalf("unparsed describe was returned: err=%v rej=%+v", err, rej)
	}

	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "filelog", Files: []string{"Foo/..."}}, root); err != nil || rej == nil {
		t.Fatalf("wildcard filelog: err=%v rej=%+v", err, rej)
	}
	if err := os.MkdirAll(filepath.Join(root, "Dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "filelog", Files: []string{"Dir"}}, root); err != nil || rej == nil || !strings.Contains(rej.Message, "individual files") {
		t.Fatalf("directory filelog: err=%v rej=%+v", err, rej)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "filelog -t -L -m 20") {
		t.Fatalf("filelog command = %s", log)
	}
	if strings.Contains(string(log), "filelog -t -L -m 20 -") {
		t.Fatalf("filelog forwarded a flag: %s", log)
	}
}

func TestWorkspaceFilelogDoesNotRegisterOrNeedATicket(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FILELOG", "//DJ01_Proj/Dev/A.cpp\n... #1 change 50 add on 2024/01/01 00:00:00 by alice@alice_DEV (text) 'add'\n")
	binding := &p4TaskBinding{
		ws:                  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
		change:              "1001",
		registrationPending: true,
		submitted:           true,
		ref:                 p4depot.Ref{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"},
	}
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "filelog", Files: []string{"A.cpp"}}, root); err != nil || rej != nil {
		t.Fatalf("filelog during pending registration: err=%v rej=%+v", err, rej)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "filelog", Max: 500, Files: []string{"A.cpp"}}, root); err != nil || rej == nil {
		t.Fatalf("oversized max: err=%v rej=%+v", err, rej)
	}
}

func TestWorkspaceSyncAcceptsIndividualFilesOnly(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_SYNC", "ok\n")
	binding := &p4TaskBinding{
		ws:  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//DJ01_Proj/Dev", AllowSync: true},
	}
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "sync", Files: []string{"A.cpp"}}, root); err != nil || rej != nil {
		t.Fatalf("file sync: err=%v rej=%+v", err, rej)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "sync", Files: []string{"A.cpp/..."}}, root); err != nil || rej == nil {
		t.Fatalf("wildcard sync: err=%v rej=%+v", err, rej)
	}
	if err := os.MkdirAll(filepath.Join(root, "Dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "sync", Files: []string{"Dir"}}, root); err != nil || rej == nil {
		t.Fatalf("directory sync: err=%v rej=%+v", err, rej)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "sync ") || strings.Contains(string(log), "//alice_DEV/...") {
		t.Fatalf("sync log = %s", log)
	}
	if !strings.Contains(string(log), "A.cpp") {
		t.Fatalf("sync did not name the file: %s", log)
	}
}
