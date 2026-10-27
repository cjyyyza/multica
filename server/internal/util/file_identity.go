package util

import "os"

// StatFileIdentity captures identity through a handle. On Windows os.Stat
// defers reading the file ID until os.SameFile, which can follow a replacement
// at the old path instead of comparing the object originally inspected.
func StatFileIdentity(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
