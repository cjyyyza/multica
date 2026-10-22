//go:build windows

package agent

import (
	"bytes"
	"os"
	"testing"
)

// POSIX script fixtures cannot be launched by CreateProcess. Native Windows
// subprocess fixtures re-execute the test binary instead.
func writeTestExecutable(tb testing.TB, path string, content []byte) {
	tb.Helper()
	if bytes.HasPrefix(content, []byte("#!")) {
		tb.Skip("requires a POSIX script executable; run this protocol fixture on Linux")
	}
	if err := os.WriteFile(path, content, 0o755); err != nil {
		tb.Fatalf("write test executable %s: %v", path, err)
	}
}
