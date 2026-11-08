package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestBindP4WorkspaceDoesNotCreateOrInspectATaskChange(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\nP4USER=alice\nP4CHARSET=utf8\n")
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName alice_DEV\n... clientRoot "+root+"\n... serverServices commit-server\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Owner alice\n... Root "+root+"\n... Stream //depot/main\n")
	d := &Daemon{logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}
	binding, err := d.bindP4Workspace(t.Context(), Task{ID: "task", P4Ticket: "123", P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}}}, &localDirectoryAssignment{AbsPath: root, RealPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if binding.envSpec().Client != "alice_DEV" || binding.envSpec().Ticket != "123" {
		t.Fatalf("env=%+v", binding.envSpec())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"change -i", "opened", "shelve", "submit", "revert", "fstat"} {
		if strings.Contains(string(log), forbidden) {
			t.Fatalf("binding ran %s: %s", forbidden, log)
		}
	}
}
