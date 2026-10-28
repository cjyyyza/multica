package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestP4WorkspaceSymlinkPaths(t *testing.T) {
	testP4WorkspaceLinkedPaths(t, func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	})
}

func testP4WorkspaceLinkedPaths(t *testing.T, link func(*testing.T, string, string)) {
	t.Helper()
	root, outside := t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(root, ".multica"), filepath.Join(outside, "sub")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "client")
	link(t, root, alias)
	link(t, filepath.Join(outside, "sub"), filepath.Join(root, "escape"))
	link(t, filepath.Join(root, ".multica"), filepath.Join(root, "private-alias"))
	b := &p4TaskBinding{
		ws: &p4cache.Workspace{Root: alias, Dir: root, Client: "client"}, ticket: "123",
		sidecars: []string{filepath.Join(alias, ".multica")},
	}
	t.Run("physical spelling and missing descendants stay inside", func(t *testing.T) {
		file := filepath.Join(root, "new", "nested", "file.cpp")
		files, _, rej := b.resolveWorkspacePaths(root, []string{file}, "")
		if rej != nil || len(files) != 1 || files[0] != filepath.Join(alias, "new", "nested", "file.cpp") {
			t.Fatalf("same workspace through its physical path was rejected: files=%v rejection=%+v", files, rej)
		}
	})
	// A missing p4 executable makes any accidental command execution fail the
	// assertion. Both guards must run before creating or changing a changelist.
	b.change = "1001"
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: filepath.Join(t.TempDir(), "missing-p4")}}
	for _, tc := range []struct {
		name, file, code string
	}{
		{"escape with missing leaf", filepath.Join(alias, "escape", "new.cpp"), p4CodeNotInWorkspace},
		{"relative dot dot after a link", "escape" + string(filepath.Separator) + ".." + string(filepath.Separator) + "new.cpp", p4CodeNotInWorkspace},
		{"sidecar through another link", filepath.Join(root, "private-alias", "new.json"), p4CodeSidecarPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: p4cache.ActionAdd, Files: []string{tc.file}}, alias)
			if err != nil || rej == nil || rej.Code != tc.code {
				t.Fatalf("write guard = %+v, %v; want %s before any p4 command", rej, err, tc.code)
			}
			if b.changelist() != "1001" || b.changeTouched {
				t.Fatal("rejected path changed the task changelist")
			}
		})
	}
	t.Run("move destination escapes", func(t *testing.T) {
		_, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{
			Action: p4cache.ActionMove, Files: []string{filepath.Join(alias, "source.cpp")},
			Dest: filepath.Join(alias, "escape", "moved.cpp"),
		}, alias)
		if err != nil || rej == nil || rej.Code != p4CodeNotInWorkspace {
			t.Fatalf("move guard = %+v, %v", rej, err)
		}
	})
	t.Run("fstat alias still protects the sidecar", func(t *testing.T) {
		rej := b.rejectFileStates(p4cache.ActionAdd, []p4cache.FileState{{LocalPath: filepath.Join(root, "private-alias", "marker.json")}})
		if rej == nil || rej.Code != p4CodeSidecarPath {
			t.Fatalf("sidecar fstat guard = %+v", rej)
		}
	})
}

func TestP4WorkspacePathsRespectFilesystemCase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are case insensitive")
	}
	base := t.TempDir()
	root, other := filepath.Join(base, "client"), filepath.Join(base, "CLIENT")
	for _, dir := range []string{root, other} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.Stat(root)
	z, _ := os.Stat(other)
	if os.SameFile(a, z) {
		t.Skip("case-insensitive filesystem")
	}
	b := &p4TaskBinding{ws: &p4cache.Workspace{Root: root, Dir: root}}
	if _, _, rej := b.resolveWorkspacePaths(root, []string{filepath.Join(other, "new.cpp")}, ""); rej == nil {
		t.Fatal("accepted a different directory differing only in case")
	}
}
