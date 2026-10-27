//go:build !windows

package wecom

import (
	"os"
	"testing"
)

func assertMediaTempFilePrivate(t *testing.T, f *os.File) {
	t.Helper()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the plaintext file is mode %v, want no group or other access", perm)
	}
}
