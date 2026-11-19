package notify

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerPopoVerificationSocketIsPrivate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker images use Linux Unix sockets")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "popo.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	var bridge DockerPopoVerificationBridge
	t.Cleanup(bridge.Close)
	if err := bridge.Listen(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %v", info.Mode())
	}
	bridge.Close()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket path after shutdown: %v", err)
	}
}

func TestDockerPopoVerificationBridge(t *testing.T) {
	var bridge DockerPopoVerificationBridge
	t.Cleanup(bridge.Close)
	const email = "user@example.test"
	const code = "314159"
	if err := bridge.SendVerificationCode(t.Context(), email, code); err == nil {
		t.Fatal("accepted a request without a Windows worker")
	}

	attach := func(provider PopoVerificationCodeSender) net.Conn {
		container, windows := net.Pipe()
		t.Cleanup(func() { _ = windows.Close() })
		go func() {
			defer windows.Close()
			_ = RunDockerPopoVerificationWorker(t.Context(), windows, windows, provider)
		}()
		bridge.attach(container)
		return windows
	}
	success := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[1] != "message_send_p2p" || argValue(args, "receiver") != email {
			t.Errorf("unexpected POPO invocation: %v", args)
		}
		message, err := os.ReadFile(strings.TrimPrefix(argValue(args, "message"), "@text:"))
		if err != nil || !strings.Contains(string(message), code) {
			t.Errorf("verification message missing: %v", err)
		}
		return popoResponse(nil), nil
	}}
	worker := attach(success)
	if err := bridge.SendVerificationCode(t.Context(), email, code); err != nil {
		t.Fatal(err)
	}
	_ = worker.Close()
	if err := bridge.SendVerificationCode(t.Context(), email, code); err == nil {
		t.Fatal("accepted a request after the Windows connection closed")
	}
	attach(Popo{Run: func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("test provider failure")
	}})
	if err := bridge.SendVerificationCode(t.Context(), email, code); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("expected an unconfirmed send, got %v", err)
	}
	attach(success)
	if err := bridge.SendVerificationCode(t.Context(), email, code); err != nil {
		t.Fatalf("reconnected Windows worker did not send: %v", err)
	}
}

func TestDockerPopoVerificationWorkerRejectsInvalidPayload(t *testing.T) {
	for _, request := range []string{
		`{"email":"group-id","code":"314159"}`,
		`{"email":"user@example.test","code":"bad"}`,
		`{"email":"user@example.test","code":"314159","command":"arbitrary"}`,
	} {
		provider := Popo{Run: func(context.Context, string, []string) ([]byte, error) {
			t.Fatal("invalid payload invoked the Windows CLI")
			return nil, nil
		}}
		var output strings.Builder
		if err := RunDockerPopoVerificationWorker(t.Context(), strings.NewReader(request), &output, provider); err == nil {
			t.Fatal("invalid verification request was accepted")
		}
	}
}
