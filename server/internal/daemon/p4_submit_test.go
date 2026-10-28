package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4SubmitPreflight(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, tc := range []struct {
		name, description, failKey, failure, stream string
		empty, allowed                              bool
	}{
		{name: "shelf and unchanged files", description: "refs #123 fix\n\nMultica: MUL-7", allowed: true},
		{name: "missing ticket", description: "fix\nMultica: MUL-7"},
		{name: "different ticket", description: "refs #456 fix\nMultica: MUL-7"},
		{name: "missing task trailer", description: "refs #123 fix"},
		{name: "open stream spec without a stream shelf", description: "refs #123 fix\nMultica: MUL-7", stream: "//depot/main"},
		{name: "empty after revert", description: "refs #123 fix\nMultica: MUL-7", empty: true},
		{name: "cannot read spec", failKey: "CHANGEOUT", failure: "connection lost"},
		{name: "revert failed", failKey: "REVERT", failure: "revert failed", description: "refs #123 fix\nMultica: MUL-7"},
		{name: "cannot read opens", failKey: "OPENED", failure: "connection lost", description: "refs #123 fix\nMultica: MUL-7"},
		{name: "shelf delete failed", failKey: "SHELFDELETE", failure: "permission denied", description: "refs #123 fix\nMultica: MUL-7"},
		{name: "no prior shelf", failKey: "SHELFDELETE", failure: "No shelved files in changelist to delete.", description: "refs #123 fix\nMultica: MUL-7", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "p4.log")
			t.Setenv("P4_FAKE_LOG", logPath)
			t.Setenv("P4_FAKE_DEPOT", "//depot/main")
			t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_main\nUser: alice\nStatus: pending\nStream: "+tc.stream+"\nDescription:\n\t"+strings.ReplaceAll(tc.description, "\n", "\n\t")+"\nFiles:\n\t//depot/main/a.cpp\n")
			t.Setenv("P4_FAKE_DESCRIBE", "... change 2002\n... oldChange 1001\n... status submitted\n... time 1700000000\n")
			t.Setenv("P4_FAKE_OPENED", "... depotFile //depot/main/a.cpp\n... change 1001\n... action edit\n")
			if tc.empty {
				t.Setenv("P4_FAKE_OPENED", "")
			}
			if tc.failKey != "" {
				t.Setenv("P4_FAKE_"+tc.failKey+"_ERR", tc.failure)
			}
			b := &p4TaskBinding{
				ws:  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_main", Root: root, Dir: root},
				ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true}, change: "1001", ticket: "123", issueKey: "MUL-7",
			}
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
			b.touchPaths([]string{filepath.Join(root, "a.cpp")})
			_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root)
			if tc.allowed != (rej == nil && err == nil) {
				t.Fatalf("submit: rejection=%+v error=%v", rej, err)
			}
			logBytes, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			log := string(logBytes)
			submit := strings.Index(log, "submit -c 1001")
			if !tc.allowed {
				if submit >= 0 || b.isSubmitted() {
					t.Fatalf("failed preflight still submitted: %s", log)
				}
				return
			}
			revert := strings.Index(log, "revert -a -c 1001 //depot/main/a.cpp")
			deleteShelf := strings.Index(log, "shelve -d -c 1001\n")
			if revert < 0 || deleteShelf <= revert || submit <= deleteShelf || strings.Count(log, "change -o 1001") != 2 {
				t.Fatalf("submit must validate, clean unchanged opens, revalidate, then delete the whole shelf: %s", log)
			}
		})
	}
}

func TestP4SyncRejectsUnsupportedRequestOptions(t *testing.T) {
	for _, req := range []p4MutateRequest{{Changelist: "1234"}, {Fresh: true}} {
		req.Action = "sync"
		b := &p4TaskBinding{ref: p4depot.Ref{AllowSync: true}}
		_, rej, err := (&Daemon{}).runWorkspaceP4(t.Context(), b, req, "")
		if err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
			t.Fatalf("unsupported sync options reached P4: %+v %+v %v", req, rej, err)
		}
	}
}

func TestP4SubmitLeavesStreamSpecsForAHuman(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CHANGEOUT", "Change: 1001\nClient: alice_main\nUser: alice\nStatus: pending\nDescription:\n\trefs #123 fix\n\tMultica: MUL-7\n")
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... stream //depot/main\n... depotFile0 //depot/main/a.cpp\n... action0 edit\n")
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_main", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowSubmit: true}, change: "1001", ticket: "123", issueKey: "MUL-7"}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: writeDaemonFakeP4(t)}}
	_, rejection, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, root)
	if err != nil || rejection == nil || rejection.Code != p4CodeActionNotAllowed || !strings.Contains(rejection.Message, "stream specification") {
		t.Fatalf("stream spec must be an explicit action rejection: %+v %v", rejection, err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"submit -c", "shelve -d", "revert -a"} {
		if strings.Contains(string(raw), mutation) {
			t.Fatalf("stream preflight allowed a mutation: %s", raw)
		}
	}
}

func TestP4UnshelveOnlyAppliesFilesFromAStreamShelf(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... stream //depot/main\n... depotFile0 //depot/main/a.cpp\n... action0 edit\n")
	t.Setenv("P4_FAKE_WHERE", "... depotFile //depot/main/a.cpp\n... path "+filepath.Join(root, "a.cpp")+"\n")
	b := &p4TaskBinding{ws: &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_main", Root: root, Dir: root},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/main", AllowUnshelve: true}, change: "1001"}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: writeDaemonFakeP4(t)}}
	_, rejection, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "unshelve"}, root)
	if err != nil || rejection != nil {
		t.Fatalf("valid shelf files were refused: %+v %v", rejection, err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), "unshelve -s 1001 -Af -c 1001") {
		t.Fatalf("unshelve did not exclude the stream spec: %s %v", raw, err)
	}
}
