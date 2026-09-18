package popo

import (
	"errors"
	"os"
	"runtime"
)

// ErrNotWindows is returned by Windows-only CLI entry points when the
// process is not running on Windows. Cloud and macOS agents must not call
// dj01bot or POPO.
var ErrNotWindows = errors.New("POPO and dj01bot are Windows-local only")

// AllowNonWindowsEnv lets tests exercise the CLI without a Windows host.
const AllowNonWindowsEnv = "MULTICA_POPO_ALLOW_NON_WINDOWS"

// RequireWindowsLocal refuses to talk to dj01bot off Windows unless the
// explicit test override is set.
func RequireWindowsLocal() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if os.Getenv(AllowNonWindowsEnv) == "1" {
		return nil
	}
	return ErrNotWindows
}
