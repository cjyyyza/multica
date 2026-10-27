//go:build !windows

package wecom

import "os"

func createMediaTempFile(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, "wecom-media-*.bin")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}
