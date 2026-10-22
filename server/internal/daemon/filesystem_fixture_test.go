package daemon

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
)

func createTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		const errorPrivilegeNotHeld syscall.Errno = 1314
		if runtime.GOOS == "windows" && errors.Is(err, errorPrivilegeNotHeld) {
			t.Skipf("file-system symlinks require Windows Developer Mode or the symbolic-link privilege: %v", err)
		}
		t.Fatal(err)
	}
}

func windowsSharingViolation(err error) bool {
	const errorSharingViolation syscall.Errno = 32
	return runtime.GOOS == "windows" && errors.Is(err, errorSharingViolation)
}
