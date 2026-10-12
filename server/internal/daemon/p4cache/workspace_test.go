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
	case rest[0] == "opened":
		emit("OPENED")
	case rest[0] == "changes":
		emit("CHANGES")
	case rest[0] == "describe":
		emit("DESCRIBE")
	case rest[0] == "change" && strings.Contains(joined, " -i "):
		io.Copy(io.Discard, os.Stdin)
		emit("CHANGEIN")
	case rest[0] == "change" && strings.Contains(joined, " -o "):
		emit("CHANGEOUT")
	case rest[0] == "change" && strings.Contains(joined, " -d "):
		emit("CHANGEDEL")
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
			name: "client does not exist",
			info: "... userName alice\n... clientName alice_DEV\n",
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

	result, err := fixture.cache.FinishChange(context.Background(), ws, "794230")
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
	if strings.Contains(log, "shelve") {
		t.Fatalf("an empty changelist was shelved: %s", log)
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

			result, err := fixture.cache.FinishChange(context.Background(), ws, "794230")
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
			wantShelve := "shelve -f -c 794230"
			if edge {
				wantShelve = "shelve -f -p -c 794230"
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

func TestDescribeChangeFollowsASubmitRenumber(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ref := fixture.detect(t).Ref()
	ref.Depot = "//DJ01_Proj/Dev"

	t.Setenv("P4_FAKE_DESCRIBE", "... change 795115\n... oldChange 795095\n... status submitted\n... user alice\n")
	info, err := fixture.cache.DescribeChange(context.Background(), ref, "795095")
	if err != nil {
		t.Fatalf("DescribeChange: %v", err)
	}
	if info.Change != "795115" || info.OldChange != "795095" || info.Status != "submitted" || info.Missing {
		t.Fatalf("info = %+v", info)
	}
	if log := fixture.log(t); !strings.Contains(log, "describe -s -O 795095") {
		t.Fatalf("describe did not look the changelist up by its original number: %s", log)
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
