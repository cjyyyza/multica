//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// writeZcodeTestScript materialises a POSIX `zcode app-server` fixture as an
// executable script named "zcode" in dir.
func writeZcodeTestScript(t *testing.T, dir, script string) string {
	t.Helper()
	bin := filepath.Join(dir, "zcode")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zcode: %v", err)
	}
	return bin
}
