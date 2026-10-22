package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetHomeUsesTestProfileOnEveryPlatform(t *testing.T) {
	home := t.TempDir()
	SetHome(t, home)
	actual, err := os.UserHomeDir()
	if err != nil || actual != home || os.Getenv("HOME") != home || os.Getenv("USERPROFILE") != home {
		t.Fatalf("home lookup escaped fixture: home=%q error=%v", actual, err)
	}
	for key, want := range map[string]string{
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s escaped fixture: got %q, want %q", key, got, want)
		}
	}
}

func TestRunIsolatedPreservesExplicitChildOverridesAndRestoresParent(t *testing.T) {
	t.Setenv(processHomeKey, "")
	host := t.TempDir()
	SetHome(t, host)
	t.Setenv("CODEX_HOME", filepath.Join(host, "codex"))
	var isolated string
	code := RunIsolated(func() int {
		isolated = os.Getenv("HOME")
		if isolated == host || os.Getenv("USERPROFILE") != isolated || os.Getenv("CODEX_HOME") != "" {
			t.Fatal("test binary inherited a host profile")
		}
		child := filepath.Join(isolated, "child")
		SetHome(t, child)
		return RunIsolated(func() int {
			if actual, _ := os.UserHomeDir(); actual != child {
				t.Fatalf("child fixture home was replaced: %q", actual)
			}
			return 7
		})
	})
	if code != 7 || os.Getenv("HOME") != host || os.Getenv("CODEX_HOME") != filepath.Join(host, "codex") {
		t.Fatal("process environment or result was not restored")
	}
	if _, err := os.Stat(isolated); !os.IsNotExist(err) {
		t.Fatalf("isolated profile survived test exit: %v", err)
	}
}
