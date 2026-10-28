package p4cache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeWorkspaceP4 builds a p4 stand-in whose answers come from the
// environment, so each test scripts exactly the server state it needs. Every
// invocation is appended to P4_FAKE_LOG as "<cwd>\t<args>".
func writeFakeWorkspaceP4(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "fake_ws_p4.go")
	program := `package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func emit(key string) {
	if body := os.Getenv("P4_FAKE_" + key + "_ERR"); body != "" {
		fmt.Fprintln(os.Stderr, body)
		fmt.Print(os.Getenv("P4_FAKE_" + key))
		os.Exit(1)
	}
	fmt.Print(os.Getenv("P4_FAKE_" + key))
}

func main() {
	args := os.Args[1:]
	cwd, _ := os.Getwd()
	if log := os.Getenv("P4_FAKE_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(f, "%s\t%s\n", cwd, strings.Join(args, " "))
		f.Close()
	}
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "-u", "-c", "-C":
			i++
		case "-ztag":
		default:
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}
	if len(rest) == 0 {
		panic("no p4 command")
	}
	joined := " " + strings.Join(rest, " ") + " "
	switch {
	case rest[0] == "set":
		emit("SET")
	case rest[0] == "info":
		emit("INFO")
	case rest[0] == "client" && strings.Contains(joined, " -o "):
		emit("CLIENT")
	case rest[0] == "fstat":
		emit("FSTAT")
	case rest[0] == "where":
		emit("WHERE")
	case rest[0] == "diff2":
		emit("DIFF2")
	case rest[0] == "opened":
		emit("OPENED")
	case rest[0] == "changes":
		emit("CHANGES")
	case rest[0] == "describe":
		if strings.Contains(joined, " -S ") {
			if _, ok := os.LookupEnv("P4_FAKE_SHELF"); ok || os.Getenv("P4_FAKE_SHELF_ERR") != "" {
				emit("SHELF")
			} else {
				fmt.Printf("... change %s\n... status pending\n", rest[len(rest)-1])
			}
			return
		}
		if original := os.Getenv("P4_FAKE_ORIGINAL_CHANGE"); original != "" && strings.Contains(joined, " " + original + " ") && !strings.Contains(joined, " -O ") {
			fmt.Fprintln(os.Stderr, "Change " + original + " unknown.")
			os.Exit(1)
		}
		emit("DESCRIBE")
	case rest[0] == "change" && strings.Contains(joined, " -i "):
		io.Copy(io.Discard, os.Stdin)
		emit("CHANGEIN")
	case rest[0] == "change" && strings.Contains(joined, " -o "):
		emit("CHANGEOUT")
	case rest[0] == "change" && strings.Contains(joined, " -d "):
		emit("CHANGEDEL")
	case rest[0] == "sync":
		emit("SYNC")
	case rest[0] == "revert":
		emit("REVERT")
	case rest[0] == "shelve":
		emit("SHELVE")
	case rest[0] == "reconcile" || rest[0] == "edit" || rest[0] == "add" || rest[0] == "delete" || rest[0] == "reopen" || rest[0] == "move":
		emit("MUTATE")
	default:
		panic("unexpected p4 command: " + joined)
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-ws-p4"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", path, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v\n%s", err, out)
	}
	return path
}

// workspaceFixture wires the fake p4 to a scripted commit server whose client
// root is the returned directory.
type workspaceFixture struct {
	cache   *Cache
	root    string
	logPath string
}

func newWorkspaceFixture(t *testing.T) *workspaceFixture {
	t.Helper()
	dir := t.TempDir()
	bin := writeFakeWorkspaceP4(t, dir)
	root := filepath.Join(dir, "DJ")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "p4.log")
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_SET", "P4PORT=ssl:p4-commit.example:1666\nP4CLIENT=alice_DEV (config)\nP4CHARSET=utf8\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+root+"\n... serverServices commit-server\n... serverCluster p4_dj01\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Host "+hostname+"\n... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	return &workspaceFixture{cache: &Cache{P4Path: bin}, root: root, logPath: logPath}
}

func (f *workspaceFixture) log(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (f *workspaceFixture) detect(t *testing.T) *Workspace {
	t.Helper()
	ws, err := f.cache.DetectWorkspace(context.Background(), f.root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	return ws
}

func TestDetectWorkspaceResolvesTheHumanClient(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)

	if ws.Port != "ssl:p4-commit.example:1666" || ws.User != "alice" || ws.Client != "alice_DEV" {
		t.Fatalf("workspace identity = %+v", ws)
	}
	if ws.Charset != "utf8" {
		t.Fatalf("charset = %q, want the value p4 set reported", ws.Charset)
	}
	if ws.Stream != "//DJ01_Proj/Dev" || ws.Root != fixture.root {
		t.Fatalf("stream/root = %q %q", ws.Stream, ws.Root)
	}
	if ws.ServerCluster != "p4_dj01" {
		t.Fatalf("server cluster = %q", ws.ServerCluster)
	}
	if ws.Edge {
		t.Fatal("a commit server was reported as an edge server")
	}

	log := fixture.log(t)
	if !strings.Contains(log, "set -q") || !strings.Contains(log, "-ztag info") {
		t.Fatalf("detection did not read the per-directory settings: %s", log)
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		cwd, args, _ := strings.Cut(line, "\t")
		if cwd != fixture.root {
			t.Fatalf("p4 %s ran in %s instead of the workspace directory", args, cwd)
		}
	}
	if !strings.Contains(log, "-p ssl:p4-commit.example:1666 -u alice -c alice_DEV -C utf8 client -o") {
		t.Fatalf("client spec was not read with an explicit connection: %s", log)
	}
}

func TestDetectWorkspaceIdentifiesAnEdgeServer(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+fixture.root+"\n... serverServices edge-server\n")
	if ws := fixture.detect(t); !ws.Edge {
		t.Fatal("edge-server was not detected, so shelves would never reach the commit server")
	}
}

func TestDetectWorkspaceRefusesAForeignClient(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		client string
		info   string
		dir    string
		want   string
	}{
		{
			name:   "host bound to another machine",
			client: "... Client alice_DEV\n... Owner alice\n... Host OTHER-MACHINE\n... Root {root}\n",
			want:   "restricted to host",
		},
		{
			name:   "owned by someone else",
			client: "... Client alice_DEV\n... Owner bob\n... Host " + hostname + "\n... Root {root}\n",
			want:   "owned by",
		},
		{
			name:   "directory outside the client root",
			client: "... Client alice_DEV\n... Owner alice\n... Root {root}\n",
			dir:    "elsewhere",
			want:   "outside the root",
		},
		{
			name: "client root cannot be verified",
			info: "... userName alice\n... clientName alice_DEV\n",
			want: "could not be verified",
		},
		{
			name: "client is explicitly unknown",
			info: "... userName alice\n... clientName unknown\n",
			want: "does not exist",
		},
		{
			name:   "unrestricted host is accepted",
			client: "... Client alice_DEV\n... Owner Alice\n... Root {root}\n... Stream //DJ01_Proj/Dev\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceFixture(t)
			if tc.client != "" {
				t.Setenv("P4_FAKE_CLIENT", strings.ReplaceAll(tc.client, "{root}", fixture.root))
			}
			if tc.info != "" {
				t.Setenv("P4_FAKE_INFO", tc.info)
			}
			dir := fixture.root
			if tc.dir != "" {
				dir = filepath.Join(filepath.Dir(fixture.root), tc.dir)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, err := fixture.cache.DetectWorkspace(context.Background(), dir)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("DetectWorkspace: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestFileStatesReportsConflicts(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_FSTAT", strings.Join([]string{
		"... depotFile //DJ01_Proj/Dev/Game/A.uasset",
		"... clientFile " + filepath.Join(fixture.root, "Game", "A.uasset"),
		"... headType binary+l",
		"... headRev 7",
		"... haveRev 5",
		"... otherOpen 1",
		"... otherOpen0 bob@bob_DEV",
		"... otherAction0 edit",
		"... otherLock",
		"",
		"... depotFile //DJ01_Proj/Dev/Game/B.cpp",
		"... headType text",
		"... headRev 2",
		"... haveRev 2",
		"... action edit",
		"... change 794229",
		"",
	}, "\n"))

	states, err := fixture.cache.FileStates(context.Background(), ws, []string{"Game/A.uasset", "Game/B.cpp"})
	if err != nil {
		t.Fatalf("FileStates: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %+v", states)
	}
	locked := states[0]
	if !locked.Exclusive() || !locked.OtherLock || len(locked.OtherOpens) != 1 || locked.OtherOpens[0] != "bob@bob_DEV" {
		t.Fatalf("exclusive conflict lost: %+v", locked)
	}
	if locked.AtHead() {
		t.Fatalf("haveRev 5 of headRev 7 reported as head: %+v", locked)
	}
	mine := states[1]
	if mine.OpenChange != "794229" || mine.Action != "edit" || mine.Exclusive() {
		t.Fatalf("own open lost: %+v", mine)
	}
	if !mine.AtHead() {
		t.Fatalf("synced file reported as behind: %+v", mine)
	}
	if log := fixture.log(t); !strings.Contains(log, "fstat Game/A.uasset Game/B.cpp") {
		t.Fatalf("fstat did not stay on the given files: %s", log)
	}
}

func TestFileStatesAcceptsFilesWithNoDepotRecord(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_FSTAT_ERR", "Game/New.cpp - no such file(s).")
	states, err := fixture.cache.FileStates(context.Background(), ws, []string{"Game/New.cpp"})
	if err != nil {
		t.Fatalf("a file about to be added must not fail fstat: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v", states)
	}
}

func TestFileStatesSurfacesRealFailures(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_FSTAT_ERR", "Perforce password (P4PASSWD) invalid or unset.")
	if _, err := fixture.cache.FileStates(context.Background(), ws, []string{"Game/A.cpp"}); err == nil {
		t.Fatal("an expired ticket was reported as an empty file list")
	}
}

func TestFileStatesRejectsPartialFailure(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/A.cpp\n... headType text\n")
	t.Setenv("P4_FAKE_FSTAT_ERR", "B.cpp - permission denied.")
	if _, err := fixture.cache.FileStates(t.Context(), ws, []string{"A.cpp", "B.cpp"}); err == nil {
		t.Fatal("partial fstat success hid a permission failure")
	}
}

func TestShelfFileMatchesHaveRequiresConfirmedContentAndType(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	const file = "//depot/main/A.cpp"
	for _, tc := range []struct {
		name, response, failure string
		identical               bool
	}{
		{name: "identical", response: "... status identical\n... depotFile " + file + "\n... depotFile2 " + file + "\n", identical: true},
		{name: "different content", response: "... status content\n... depotFile " + file + "\n... depotFile2 " + file + "\n"},
		{name: "different type", response: "... status types\n... depotFile " + file + "\n... depotFile2 " + file + "\n"},
		{name: "wrong file", response: "... status identical\n... depotFile //depot/main/B.cpp\n... depotFile2 " + file + "\n"},
		{name: "empty response"},
		{name: "partial failure", response: "... status identical\n... depotFile " + file + "\n... depotFile2 " + file + "\n", failure: "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("P4_FAKE_DIFF2", tc.response)
			t.Setenv("P4_FAKE_DIFF2_ERR", tc.failure)
			same, err := fixture.cache.ShelfFileMatchesHave(t.Context(), ws, "1001", FileState{DepotFile: file, Action: "edit"})
			if same != tc.identical || (err != nil) != (tc.failure != "") {
				t.Fatalf("same=%v error=%v", same, err)
			}
		})
	}
}

func TestPendingChangeDescriptionPreservesHumanFormatting(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	const description = "Human summary\n\n\tIndented details\n#review-42\nMultica: MUL-7"
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_DEV\nUser: alice\nStatus: pending\nDescription:\n\t"+strings.ReplaceAll(description, "\n", "\n\t")+"\n")
	got, err := fixture.cache.ReadPendingChange(t.Context(), ws, "1001")
	if err != nil || got.Description != description {
		t.Fatalf("description=%q error=%v", got.Description, err)
	}
}

func TestFinishChangePreservesExistingShelf(t *testing.T) {
	for _, tc := range []struct {
		name, opened, failure string
	}{
		{name: "shelf only"},
		{name: "shelf and new open", opened: "... depotFile //DJ01_Proj/Dev/B.cpp\n... action edit\n... change 794230\n"},
		{name: "shelf lookup failure", failure: "connection lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceFixture(t)
			ws := fixture.detect(t)
			t.Setenv("P4_FAKE_OPENED", tc.opened)
			t.Setenv("P4_FAKE_FSTAT", tc.opened)
			t.Setenv("P4_FAKE_SHELF", "... change 794230\n... status pending\n... depotFile0 //DJ01_Proj/Dev/A.cpp\n... action0 edit\n... type0 text\n")
			t.Setenv("P4_FAKE_SHELF_ERR", tc.failure)
			result, err := fixture.cache.FinishChange(t.Context(), ws, "794230", FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
			log := fixture.log(t)
			for _, forbidden := range []string{"shelve -d", "shelve -r", "change -d"} {
				if strings.Contains(log, forbidden) {
					t.Fatalf("existing shelf was destroyed by %q:\n%s", forbidden, log)
				}
			}
			if tc.failure != "" {
				if err == nil || strings.Contains(log, "shelve -f") {
					t.Fatalf("failed shelf lookup allowed a write: result=%+v error=%v\n%s", result, err, log)
				}
				return
			}
			wantFiles := 1
			if tc.opened != "" {
				wantFiles = 2
				if !strings.Contains(log, "shelve -f -Af -c 794230 //DJ01_Proj/Dev/B.cpp") {
					t.Fatalf("new file was not shelved without replacing shelf-only files:\n%s", log)
				}
			} else if strings.Contains(log, "shelve -f") {
				t.Fatalf("an untouched shelf was rewritten:\n%s", log)
			}
			if err != nil || result == nil || !result.Shelved || result.Deleted || len(result.Files) != wantFiles {
				t.Fatalf("result=%+v error=%v, want %d preserved files", result, err, wantFiles)
			}
		})
	}
}

func TestFinishChangeRefusesUnverifiedShelf(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	for _, response := range []string{
		"",
		"... change 999\n... status pending\n",
		"... change 794230\n... status submitted\n",
		"... change 794230\n... status pending\n... depotFile1 //depot/A.cpp\n",
		"... change 794230\n... status pending\n... depotFile0 \n",
		"... change 794230\n... status pending\n... stream invalid\n",
	} {
		t.Run(response, func(t *testing.T) {
			t.Setenv("P4_FAKE_SHELF", response)
			before := fixture.log(t)
			if _, err := fixture.cache.FinishChange(t.Context(), ws, "794230", FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}}); err == nil {
				t.Fatal("unverified shelf was treated as empty")
			}
			log := strings.TrimPrefix(fixture.log(t), before)
			for _, verb := range []string{"revert ", "shelve ", "change -d"} {
				if strings.Contains(log, verb) {
					t.Fatalf("invalid shelf response allowed mutation:\n%s", log)
				}
			}
		})
	}
}

func TestFinishChangePreservesStreamShelfAndValidFiles(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	for _, files := range []string{"", "... depotFile0 //DJ01_Proj/Dev/A.cpp\n... action0 edit\n... type0 text\n"} {
		t.Run(files, func(t *testing.T) {
			t.Setenv("P4_FAKE_SHELF", "... change 794230\n... status pending\n... stream //DJ01_Proj/Dev\n"+files)
			before := fixture.log(t)
			result, err := fixture.cache.FinishChange(t.Context(), ws, "794230", FinishChangeOptions{})
			if err != nil || !result.Shelved || result.Deleted || (len(result.Files) > 0) != (files != "") {
				t.Fatalf("valid stream shelf was not retained: %+v %v", result, err)
			}
			if log := strings.TrimPrefix(fixture.log(t), before); strings.Contains(log, "change -d") || strings.Contains(log, "shelve -d") {
				t.Fatalf("stream shelf was treated as an empty changelist: %s", log)
			}
		})
	}
}

func TestShelveFilesDoesNotShelveAnOpenStreamSpec(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	if _, err := fixture.cache.ShelveFiles(t.Context(), ws, "794230", []string{"//DJ01_Proj/Dev/A.cpp"}); err != nil {
		t.Fatal(err)
	}
	if log := fixture.log(t); !strings.Contains(log, "shelve -f -Af -c 794230 //DJ01_Proj/Dev/A.cpp") {
		t.Fatalf("shelve did not limit its operation to files: %s", log)
	}
}

func TestFindTaskChangeReusesTheIssueChangelist(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_CHANGES", strings.Join([]string{
		"Change 794100 on 2026/10/01 by alice@alice_DEV *pending*",
		"",
		"\trefs #5001 unrelated work",
		"",
		"Change 794229 on 2026/10/12 by alice@alice_DEV *pending*",
		"",
		"\trefs #5002 fix the crash",
		"",
		"\tMultica: DJ-123",
		"",
	}, "\n"))

	change, err := fixture.cache.FindTaskChange(context.Background(), ws, "DJ-123")
	if err != nil {
		t.Fatalf("FindTaskChange: %v", err)
	}
	if change != "794229" {
		t.Fatalf("change = %q, want the changelist carrying the issue trailer", change)
	}
	missing, err := fixture.cache.FindTaskChange(context.Background(), ws, "DJ-999")
	if err != nil || missing != "" {
		t.Fatalf("change = %q, err = %v, want no reuse for an unrelated issue", missing, err)
	}
	if log := fixture.log(t); !strings.Contains(log, "changes -s pending -c alice_DEV -l") {
		t.Fatalf("pending changes were not scoped to the client: %s", log)
	}
}

func TestCreateTaskChangeReturnsTheNumber(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_CHANGEIN", "Change 794230 created.\n")
	change, err := fixture.cache.CreateTaskChange(context.Background(), ws, "refs #5002 fix the crash\n\nMultica: DJ-123\n")
	if err != nil {
		t.Fatalf("CreateTaskChange: %v", err)
	}
	if change != "794230" {
		t.Fatalf("change = %q", change)
	}
}

func TestFinishChangeDeletesAnEmptyChangelist(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_OPENED", "")
	t.Setenv("P4_FAKE_CHANGEDEL", "Change 794230 deleted.\n")

	result, err := fixture.cache.FinishChange(context.Background(), ws, "794230", FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
	if err != nil {
		t.Fatalf("FinishChange: %v", err)
	}
	if !result.Deleted || result.Shelved {
		t.Fatalf("result = %+v, want the empty changelist deleted and nothing shelved", result)
	}
	log := fixture.log(t)
	if !strings.Contains(log, "revert -a -c 794230 //alice_DEV/...") {
		t.Fatalf("unchanged opens were not reverted inside the task changelist: %s", log)
	}
	if !strings.Contains(log, "change -d 794230") {
		t.Fatalf("empty changelist was left behind: %s", log)
	}
	if strings.Contains(log, "shelve -d") {
		t.Fatalf("an empty CL must not delete a shelf: %s", log)
	}
}

func TestFinishChangeShelvesRemainingWork(t *testing.T) {
	for _, edge := range []bool{false, true} {
		name := "commit server"
		if edge {
			name = "edge server"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceFixture(t)
			if edge {
				t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+fixture.root+"\n... serverServices edge-server\n")
			}
			ws := fixture.detect(t)
			t.Setenv("P4_FAKE_OPENED", "... depotFile //DJ01_Proj/Dev/Game/A.uasset\n... action edit\n... change 794230\n... type binary+l\n")
			t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Game/A.uasset\n... headType binary+l\n... headRev 9\n... haveRev 8\n... action edit\n... change 794230\n")

			result, err := fixture.cache.FinishChange(context.Background(), ws, "794230", FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
			if err != nil {
				t.Fatalf("FinishChange: %v", err)
			}
			if !result.Shelved || result.Deleted {
				t.Fatalf("result = %+v, want the changelist shelved and kept", result)
			}
			if len(result.ExclusiveHeld) != 1 || len(result.NotHead) != 1 {
				t.Fatalf("report lost the +l and not-at-head warnings: %+v", result)
			}
			log := fixture.log(t)
			wantShelve := "shelve -f -Af -c 794230 //DJ01_Proj/Dev/Game/A.uasset"
			if edge {
				wantShelve = "shelve -f -p -Af -c 794230 //DJ01_Proj/Dev/Game/A.uasset"
			}
			if !strings.Contains(log, wantShelve) {
				t.Fatalf("expected %q in %s", wantShelve, log)
			}
			if !edge && strings.Contains(log, "-p -c 794230") {
				t.Fatalf("a commit-server shelve promoted: %s", log)
			}
			if strings.Contains(log, "change -d") {
				t.Fatalf("a changelist with work in it was deleted: %s", log)
			}
		})
	}
}

func TestSetChangeDescriptionKeepsTheFileList(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_CHANGEOUT", strings.Join([]string{
		"Change:\t794230",
		"",
		"Client:\talice_DEV",
		"",
		"Description:",
		"\tolder text",
		"\tsecond line",
		"",
		"Files:",
		"\t//DJ01_Proj/Dev/Game/A.uasset\t# edit",
		"",
	}, "\n"))
	t.Setenv("P4_FAKE_CHANGEIN", "Change 794230 updated.\n")
	if err := fixture.cache.SetChangeDescription(context.Background(), ws, "794230", "refs #5002 fix the crash\n\nMultica: DJ-123"); err != nil {
		t.Fatalf("SetChangeDescription: %v", err)
	}
	updated := replaceSpecDescription(os.Getenv("P4_FAKE_CHANGEOUT"), "refs #5002 fix the crash\n\nMultica: DJ-123")
	if !strings.Contains(updated, "\trefs #5002 fix the crash") || strings.Contains(updated, "older text") {
		t.Fatalf("description was not replaced: %s", updated)
	}
	if !strings.Contains(updated, "//DJ01_Proj/Dev/Game/A.uasset") || !strings.Contains(updated, "Client:\talice_DEV") {
		t.Fatalf("rewriting the description dropped other spec fields: %s", updated)
	}
}

func TestRunInChangeAlwaysBindsTheChangelist(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_MUTATE", "//DJ01_Proj/Dev/Game/A.uasset#8 - opened for edit\n")
	out, err := fixture.cache.RunInChange(context.Background(), ws, "794230", "edit", []string{"Game/A.uasset"})
	if err != nil {
		t.Fatalf("RunInChange: %v", err)
	}
	if !strings.Contains(out, "opened for edit") {
		t.Fatalf("output = %q", out)
	}
	if log := fixture.log(t); !strings.Contains(log, "edit -c 794230 Game/A.uasset") {
		t.Fatalf("edit was not bound to the task changelist: %s", log)
	}
	if _, err := fixture.cache.RunInChange(context.Background(), ws, "", "edit", []string{"Game/A.uasset"}); err == nil {
		t.Fatal("a write with no changelist was allowed, which would land in the person's default changelist")
	}
}

func TestMappedFileUsesOnlyOneEffectiveMapping(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	path := filepath.Join(fixture.root, "effective", "file.cpp")
	live := "... depotFile //depot/main/file.cpp\n... path " + path + "\n"
	for _, tc := range []struct {
		name, output, wantError string
	}{
		{"excluded then live", "... depotFile -//depot/main/file.cpp\n... unmap\n... path " + filepath.Join(fixture.root, "old") + "\n" + live, ""},
		{"identical live records", live + live, ""},
		{"excluded only", "... depotFile -//depot/main/file.cpp\n... unmap\n... path " + path + "\n", "not mapped"},
		{"ambiguous overlay", live + "... depotFile //depot/main/file.cpp\n... path " + filepath.Join(fixture.root, "another") + "\n", "ambiguous"},
		{"relative target", "... depotFile //depot/main/file.cpp\n... path relative.cpp\n", "invalid local path"},
		{"empty result", "", "not mapped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("P4_FAKE_WHERE", tc.output)
			depot, local, err := fixture.cache.MappedFile(t.Context(), ws, "//depot/main/file.cpp")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || depot != "//depot/main/file.cpp" || local != path {
				t.Fatalf("effective mapping = %s -> %s: %v", depot, local, err)
			}
		})
	}
}

func TestDescribeChangeFollowsASubmitRenumber(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ref := fixture.detect(t).Ref()
	ref.Depot = "//DJ01_Proj/Dev"

	t.Setenv("P4_FAKE_DESCRIBE", "... change 795115\n... oldChange 795095\n... status submitted\n... user alice\n... time 1700000000\n")
	t.Setenv("P4_FAKE_ORIGINAL_CHANGE", "795095")
	info, err := fixture.cache.DescribeChange(context.Background(), ref, "795095")
	if err != nil {
		t.Fatalf("DescribeChange: %v", err)
	}
	if info.Change != "795115" || info.OldChange != "795095" || info.Status != "submitted" || info.Missing {
		t.Fatalf("info = %+v", info)
	}
	if info.SubmittedAt.Unix() != 1700000000 {
		t.Fatalf("submission time = %v", info.SubmittedAt)
	}
	if log := fixture.log(t); !strings.Contains(log, "describe -s -O 795095") {
		t.Fatalf("describe did not look the changelist up by its original number: %s", log)
	}
	current, err := fixture.cache.DescribeChange(context.Background(), ref, "795115")
	if err != nil || current.Change != "795115" || current.OldChange != "795095" || current.Missing {
		t.Fatalf("submitted number did not resolve to the original identity: %+v %v", current, err)
	}

	t.Setenv("P4_FAKE_DESCRIBE", "")
	t.Setenv("P4_FAKE_DESCRIBE_ERR", "795095 - no such changelist.")
	deleted, err := fixture.cache.DescribeChange(context.Background(), ref, "795095")
	if err != nil {
		t.Fatalf("DescribeChange: %v", err)
	}
	if !deleted.Missing {
		t.Fatalf("a deleted changelist was not reported as missing: %+v", deleted)
	}
}

func TestSyncClientIncludesImportsInTheClientView(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	for _, stream := range []string{"", "//DJ01_Proj/Dev"} {
		ws := &Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: fixture.root, Dir: fixture.root, Stream: stream}
		if _, err := fixture.cache.SyncClient(t.Context(), ws, nil); err != nil {
			t.Fatal(err)
		}
	}
	log := fixture.log(t)
	if strings.Count(log, "sync //alice_DEV/...") != 2 || strings.Contains(log, "sync //DJ01_Proj/Dev/...") {
		t.Fatalf("sync must include all client mappings, including imported engine files: %s", log)
	}
}

func TestPendingChangeDescriptionValidatesLiveOwnership(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := &Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: fixture.root, Dir: fixture.root}
	const spec = "Change: 1001\r\nClient: alice_DEV\r\nUser: alice\r\nStatus: pending\r\nDescription:\r\n\tA summary\r\n\trefs #123 fix\r\n\tMultica: MUL-7\r\n\r\nFiles:\r\n\t//depot/main/a.cpp\r\n"
	for _, tc := range []struct{ name, before, after string }{
		{"valid", "", ""},
		{"different client", "Client: alice_DEV", "Client: other"},
		{"different owner", "User: alice", "User: bob"},
		{"submitted", "Status: pending", "Status: submitted"},
		{"missing change", "Change: 1001", "Change: new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := spec
			if tc.before != "" {
				text = strings.Replace(text, tc.before, tc.after, 1)
			}
			t.Setenv("P4_FAKE_CHANGEOUT", text)
			spec, err := fixture.cache.ReadPendingChange(t.Context(), ws, "1001")
			if tc.name != "valid" {
				if err == nil {
					t.Fatalf("accepted changelist outside the bound pending client: %s", text)
				}
				return
			}
			if err != nil || spec.Description != "A summary\nrefs #123 fix\nMultica: MUL-7" {
				t.Fatalf("multiline description = %q, error %v", spec.Description, err)
			}
		})
	}
}

func TestSubmittedChangeReadsTheOriginalIdentityInTheBoundWorkspace(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_ORIGINAL_CHANGE", "1001")
	for _, tc := range []struct {
		name, body, stderr, want string
	}{
		{"renumbered", "... change 2002\n... oldChange 1001\n... status submitted\n... time 1700000000\n", "", "2002"},
		{"same number", "... change 1001\n... status submitted\n... time 1700000000\n", "", "1001"},
		{"missing timestamp", "... change 1001\n... status submitted\n", "", ""},
		{"invalid timestamp", "... change 1001\n... status submitted\n... time nope\n", "", ""},
		{"pending", "... change 1001\n... status pending\n", "", ""},
		{"invalid number", "... change nope\n... status submitted\n", "", ""},
		{"foreign original", "... change 2002\n... oldChange 999\n... status submitted\n", "", ""},
		{"missing", "", "Change 1001 unknown.", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("P4_FAKE_DESCRIBE", tc.body+"... depotFile0 //DJ01_Proj/Dev/A.cpp\n... action0 edit\n... type0 text\n... depotFile1 //DJ01_Proj/Dev/B.cpp\n... action1 add\n... type1 text\n")
			t.Setenv("P4_FAKE_DESCRIBE_ERR", tc.stderr)
			got, err := fixture.cache.SubmittedChange(t.Context(), ws, "1001")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unverified submission accepted: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Change != tc.want || got.OriginalChange != "1001" || !got.Submitted || got.SubmittedAt.Unix() != 1700000000 || len(got.Files) != 2 || got.Files[1].Action != "add" {
				t.Fatalf("submission receipt = %+v", got)
			}
		})
	}
	if log := fixture.log(t); !strings.Contains(log, ws.Dir+"\t-ztag -p "+ws.Port+" -u alice -c alice_DEV -C utf8 describe -s -O 1001") {
		t.Fatalf("submission read did not preserve the workspace connection: %s", log)
	}
}
