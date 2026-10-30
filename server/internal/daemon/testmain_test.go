// Copyright 2026 Multica. All rights reserved.

package daemon

import (
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/testenv"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// redirectTestHome keeps the older per-test helper name available to the tests
// that redirect HOME/USERPROFILE individually.
var redirectTestHome = testutil.RedirectUserHome

// TestMain redirects the home environment variables to one scratch directory
// for the whole binary before any test runs. Config paths resolve through
// os.UserHomeDir — USERPROFILE on Windows, HOME elsewhere — while Hermes uses
// LOCALAPPDATA on Windows. Process-wide redirection isolates tests that forget
// their own redirect; per-test t.Setenv overrides still take precedence.
func TestMain(m *testing.M) {
	if testutil.RunPiCleanupFixture() {
		os.Exit(0)
	}
	for _, key := range []string{
		"MULTICA_AGENT_ID",
		"MULTICA_TASK_ID",
		"MULTICA_TOKEN",
		"MULTICA_DAEMON_PORT",
		"MULTICA_WORKSPACE_ID",
		"MULTICA_SERVER_URL",
		"MULTICA_TASK_CONFIG_ROOT",
	} {
		os.Unsetenv(key)
	}

	os.Exit(testenv.RunIsolated(m.Run))
}
