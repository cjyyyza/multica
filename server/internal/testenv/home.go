// Package testenv isolates host-owned state for tests. Import it only from tests.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const processHomeKey = "MULTICA_TEST_PROCESS_HOME"

// SetHome keeps Go's Windows home lookup and Unix-style CLI lookups in sync.
func SetHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// RunIsolated gives a test binary a disposable profile before any test runs.
// Re-executed test helpers inherit the isolated environment, including overrides
// their parent test deliberately set; they must not replace those overrides.
func RunIsolated(run func() int) int {
	if inherited := os.Getenv(processHomeKey); inherited != "" && strings.HasPrefix(filepath.Base(inherited), "multica-test-home-") {
		if info, err := os.Stat(inherited); err == nil && info.IsDir() {
			return run()
		}
	}
	home, err := os.MkdirTemp("", "multica-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test home:", err)
		return 1
	}
	defer os.RemoveAll(home)

	values := map[string]string{
		processHomeKey:    home,
		"HOME":            home,
		"USERPROFILE":     home,
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
	}
	for _, key := range []string{
		"CODEX_HOME", "CLAUDE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR",
		"HERMES_HOME", "OPENCLAW_HOME", "OPENCLAW_CONFIG_PATH", "OPENCLAW_STATE_DIR",
		"CLAWDBOT_CONFIG_PATH", "CLAWDBOT_STATE_DIR", "MOLTBOT_CONFIG_PATH", "MOLTBOT_STATE_DIR",
		"MULTICA_TASK_CONFIG_ROOT",
		"MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_DAEMON_PORT",
		"MULTICA_WORKSPACE_ID", "MULTICA_AGENT_ID", "MULTICA_TASK_ID",
	} {
		values[key] = ""
	}
	for key, value := range values {
		previous, present := os.LookupEnv(key)
		defer func() {
			if present {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		}()
		if value == "" {
			err = os.Unsetenv(key)
		} else {
			err = os.Setenv(key, value)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "isolate test environment:", key, err)
			return 1
		}
	}
	return run()
}
