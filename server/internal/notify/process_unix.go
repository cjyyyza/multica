//go:build !windows

package notify

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func configureProcess(*exec.Cmd) {}

func lockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("notification state is in use: %w", err)
	}
	return file, nil
}
