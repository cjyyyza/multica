package p4cache

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func writeFakeP4(t *testing.T, dir string) string {
	t.Helper()
	name := "p4"
	script := `#!/bin/sh
log="${P4_FAKE_LOG:-$PWD/p4-fake.log}"
printf '%s\n' "$*" >> "$log"
for a in "$@"; do
  if [ "$a" = "-i" ]; then
    cat >> "${P4_FAKE_SPEC:-$PWD/p4-fake.spec}"
  fi
done
if printf ' %s ' "$*" | grep -q ' info '; then
  echo "User name: testuser"
  echo "Client name: none"
  echo "Server address: perforce:1666"
  exit 0
fi
if printf ' %s ' "$*" | grep -q ' sync '; then
  root="${P4_FAKE_ROOT:-.}"
  mkdir -p "$root"
  echo synced > "$root/.p4synced"
  exit 0
fi
exit 0
`
	if runtime.GOOS == "windows" {
		name = "p4.cmd"
		script = `@echo off
echo %*>> "%P4_FAKE_LOG%"
exit /b 0
`
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake p4: %v", err)
	}
	return path
}

func TestSyncCreatesClientAndSyncs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake p4 script is POSIX")
	}
	dir := t.TempDir()
	bin := writeFakeP4(t, dir)
	logPath := filepath.Join(dir, "p4.log")
	specPath := filepath.Join(dir, "client.spec")
	work := filepath.Join(dir, "work")
	syncRoot := filepath.Join(work, "p4", "depot_proj")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_SPEC", specPath)
	t.Setenv("P4_FAKE_ROOT", syncRoot)
	t.Setenv("P4USER", "alice")

	cache := &Cache{P4Path: bin}
	result, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		WorkDir:     work,
		Ref:         p4depot.Ref{Port: "ssl:perforce.example.com:1666", Depot: "//depot/proj"},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Path != syncRoot {
		t.Fatalf("Path = %q, want %q", result.Path, syncRoot)
	}
	if !strings.HasPrefix(result.Client, "mc") {
		t.Fatalf("Client = %q", result.Client)
	}

	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logBody), "client -i") {
		t.Fatalf("expected client -i in %s", logBody)
	}
	if !strings.Contains(string(logBody), "sync //depot/proj/...") {
		t.Fatalf("expected sync in %s", logBody)
	}

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), "Owner: alice") {
		t.Fatalf("spec missing owner: %s", spec)
	}
	if !strings.Contains(string(spec), "//depot/proj/...") {
		t.Fatalf("spec missing view: %s", spec)
	}
	if _, err := os.Stat(filepath.Join(syncRoot, ".p4synced")); err != nil {
		t.Fatalf("sync marker: %v", err)
	}
}

func TestSyncUsesStreamSpec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake p4 script is POSIX")
	}
	dir := t.TempDir()
	bin := writeFakeP4(t, dir)
	specPath := filepath.Join(dir, "client.spec")
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", specPath)
	t.Setenv("P4_FAKE_ROOT", filepath.Join(dir, "work", "p4", "streams_main"))
	t.Setenv("P4USER", "alice")

	cache := &Cache{P4Path: bin}
	if _, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		WorkDir:     filepath.Join(dir, "work"),
		Ref: p4depot.Ref{
			Port:   "perforce:1666",
			Depot:  "//streams/main",
			Stream: "//streams/main",
		},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), "Stream: //streams/main") {
		t.Fatalf("spec missing stream: %s", spec)
	}
	if strings.Contains(string(spec), "View:") {
		t.Fatalf("stream spec should not set View: %s", spec)
	}
}

func TestSyncRejectsInvalidRef(t *testing.T) {
	cache := &Cache{P4Path: "/nonexistent/p4"}
	_, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     t.TempDir(),
		Ref:         p4depot.Ref{Port: "not a port", Depot: "nope"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
