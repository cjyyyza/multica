//go:build linux

package agent

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func requireCursorBackgroundGroupSignals(t *testing.T) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), cursorFakeModeEnv+"=leaf", "CURSOR_FAKE_DURATION=30s")
	configureProcessGroup(cmd)
	if err := startOwnedProcessTree(cmd, slog.Default()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		signalProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
		releaseProcessGroup(cmd)
	}()
	group, err := captureCursorUnixGroup(cmd.Process.Pid)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		if group != nil {
			t.Fatal("unsupported group signalling retained a handle")
		}
		t.Skipf("kernel lacks pidfd process-group signalling (Linux 6.9+); ownership correctly fails closed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	group.close()
}
