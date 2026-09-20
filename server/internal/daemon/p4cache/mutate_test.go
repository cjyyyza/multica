package p4cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestMutateEditChecksOutFileInClientRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	work := filepath.Join(dir, "work")
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	synced, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws", TaskID: "task", WorkDir: work, Ref: ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(synced.Path, "Source.cpp")
	if err := os.WriteFile(target, []byte("code"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := cache.Mutate(context.Background(), MutateParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     work,
		Cwd:         synced.Path,
		Ref:         ref,
		Action:      ActionEdit,
		Files:       []string{"Source.cpp"},
		Changelist:  "1001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Client != synced.Client || result.Action != ActionEdit {
		t.Fatalf("result = %+v", result)
	}
	logBody, err := os.ReadFile(filepath.Join(dir, "p4.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "edit -c 1001") {
		t.Fatalf("expected edit -c in %s", logBody)
	}
	if !strings.Contains(string(logBody), target) {
		t.Fatalf("expected absolute file path in %s", logBody)
	}
}

func TestMutateRejectsFileOutsideCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	work := filepath.Join(dir, "work")
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	if _, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws", TaskID: "task", WorkDir: work, Ref: ref,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := cache.Mutate(context.Background(), MutateParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     work,
		Cwd:         work,
		Ref:         ref,
		Action:      ActionEdit,
		Files:       []string{filepath.Join(dir, "outside.cpp")},
	})
	if err == nil || !strings.Contains(err.Error(), "outside the Perforce checkout") {
		t.Fatalf("error = %v", err)
	}
}

func TestMutateChangeCreatesNumberedChangelist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	work := filepath.Join(dir, "work")
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	if _, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws", TaskID: "task", WorkDir: work, Ref: ref,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := cache.Mutate(context.Background(), MutateParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     work,
		Ref:         ref,
		Action:      ActionChange,
		Description: "fix the crash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changelist != "1001" {
		t.Fatalf("changelist = %q", result.Changelist)
	}
}

func TestMutateRequiresSyncFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	_, err := cache.Mutate(context.Background(), MutateParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     t.TempDir(),
		Ref:         p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"},
		Action:      ActionOpened,
	})
	if err == nil || !strings.Contains(err.Error(), "p4 sync") {
		t.Fatalf("error = %v", err)
	}
}

func TestDepotPathAllowed(t *testing.T) {
	if !depotPathAllowed("//depot/UE/Source.cpp", "//depot/UE") {
		t.Fatal("expected in-depot path")
	}
	if depotPathAllowed("//other/x", "//depot/UE") {
		t.Fatal("expected out-of-depot path rejected")
	}
}
