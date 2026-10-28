//go:build windows

package daemon

import (
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestP4WorkspaceJunctionPaths(t *testing.T) {
	testP4WorkspaceLinkedPaths(t, createJunction)
}

func TestP4WorkspaceRejectsUnresolvableWindowsPaths(t *testing.T) {
	root := t.TempDir()
	b := &p4TaskBinding{ws: &p4cache.Workspace{Root: root, Dir: root}}
	for _, path := range []string{`C:relative.cpp`, `\root-relative.cpp`, `\\?\GLOBALROOT\Device\HarddiskVolume1\file.cpp`} {
		if _, _, rej := b.resolveWorkspacePaths(root, []string{path}, ""); rej == nil {
			t.Errorf("accepted a path without a verifiable workspace location: %s", path)
		}
	}
}

func TestDetectP4WorkspaceJunctionRoot(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "client")
	createJunction(t, root, alias)
	t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\nP4USER=alice\n")
	t.Setenv("P4_FAKE_INFO", "... clientName client\n... clientRoot "+alias+"\n... userName alice\n")
	t.Setenv("P4_FAKE_CLIENT", "... Client client\n... Owner alice\n... Root "+alias+"\n")
	c := &p4cache.Cache{P4Path: writeDaemonFakeP4(t)}
	ws, err := c.DetectWorkspace(t.Context(), root)
	if err != nil {
		t.Fatalf("physical directory of a junctioned client root was rejected: %v", err)
	}
	if ws.Root != alias || ws.Dir != root {
		t.Fatalf("P4's logical paths must be preserved: %+v", ws)
	}
}
