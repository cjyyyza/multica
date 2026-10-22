//go:build !windows

package qoderruntime

import (
	"os"
	"testing"
)

func makeRepositoryTokenPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPublicRepositoryTokenRejected(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepositoryToken(path); err == nil {
		t.Fatal("accepted public credential file")
	}
}
