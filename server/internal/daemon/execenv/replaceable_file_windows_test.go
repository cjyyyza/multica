//go:build windows

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPrepareCodexHomeFailsClosedWhenWindowsLocksSandboxConfig(t *testing.T) {
	shared, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", shared)
	if err := os.WriteFile(filepath.Join(shared, "config.toml"), []byte("windows.sandbox = \"unelevated\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.toml")
	stale := multicaManagedBeginMarker + "\nsandbox_mode = \"danger-full-access\"\n" + multicaManagedEndMarker + "\n"
	if err := os.WriteFile(config, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(config)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	err = prepareCodexHomeWithOpts(home, CodexHomeOptions{GOOS: "windows", CodexVersion: "0.144.5"}, testLogger())
	if err == nil || !strings.Contains(err.Error(), "sandbox config") {
		t.Fatalf("locked sandbox configuration did not prevent startup: %v", err)
	}
	if data, err := os.ReadFile(config); err != nil || string(data) != stale {
		t.Fatalf("blocked configuration was altered: %q %v", data, err)
	}
}

func TestCleanupSidecarsSurfacesWindowsDirectoryLocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "owned")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := sidecarManifest{Dirs: []string{dir}}
	name, _ := windows.UTF16PtrFromString(dir)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = rollBackManifest(m, "")
	_ = windows.CloseHandle(handle)
	if err == nil {
		t.Fatal("directory lock was incorrectly reported as successful cleanup")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("locked directory was not retained: %v", err)
	}
	if err := rollBackManifest(m, ""); err != nil {
		t.Fatalf("cleanup did not recover after release: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("owned directory survived successful retry: %v", err)
	}
}

func TestReplaceFilePreservesOldCacheWhileAnExternalReaderBlocksIt(t *testing.T) {
	dir := t.TempDir()
	target, source := filepath.Join(dir, "cache.json"), filepath.Join(dir, "pending.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(target)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = windows.CloseHandle(handle)
		}
	})
	if err := replaceFile(source, target); err == nil {
		t.Fatal("replacement unexpectedly bypassed a deny-delete reader")
	}
	if data, err := readReplaceableFile(target); err != nil || string(data) != "old" {
		t.Fatalf("failed replacement changed old cache: %q %v", data, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := replaceFile(source, target); err != nil {
		t.Fatal(err)
	}
	if data, err := readReplaceableFile(target); err != nil || string(data) != "new" {
		t.Fatalf("replacement not published: %q %v", data, err)
	}
}

func TestReplaceFileWaitsForATransientExternalReader(t *testing.T) {
	dir := t.TempDir()
	target, source := filepath.Join(dir, "cache"), filepath.Join(dir, "pending")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(target)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { time.Sleep(25 * time.Millisecond); _ = windows.CloseHandle(handle); close(done) }()
	err = replaceFile(source, target)
	<-done
	if err != nil {
		t.Fatalf("transient reader was not tolerated: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("cache was not committed: %q %v", data, err)
	}
}
