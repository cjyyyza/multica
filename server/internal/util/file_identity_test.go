package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatFileIdentitySurvivesReplacementAtTheSamePath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := StatFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := StatFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("identity followed the replacement instead of the inspected directory")
	}
	moved, err := StatFileIdentity(path + "-moved")
	if err != nil || !os.SameFile(before, moved) {
		t.Fatalf("original identity was lost: %v", err)
	}
}
