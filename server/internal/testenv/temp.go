package testenv

import (
	"os"
	"testing"
)

// ShortTempDir avoids duplicating long test names in local Git URL cache paths.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "multica-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove fixture directory: %v", err)
		}
	})
	return dir
}
