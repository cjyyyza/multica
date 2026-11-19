package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DockerPopoVerificationEnv    = "MULTICA_POPO_DOCKER_BRIDGE"
	DockerPopoVerificationLabel  = "io.multica.popo-docker-bridge"
	DockerPopoVerificationSocket = "/run/multica/popo-verification.sock"
	dockerPopoProtocolVersion    = 1
)

type dockerPopoHello struct {
	Version int `json:"version"`
}

type dockerPopoReceipt struct {
	Accepted *bool `json:"accepted"`
}

type dockerPopoWorker struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
	slot    chan struct{}
}

// DockerPopoVerificationBridge exchanges codes only through the container's
// private Unix socket. The Windows worker enters through a local Docker exec.
type DockerPopoVerificationBridge struct {
	mu       sync.Mutex
	listener net.Listener
	worker   *dockerPopoWorker
	closed   bool
}

func (b *DockerPopoVerificationBridge) Listen(path string) error {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() || parent.Mode().Perm() != 0700 {
		return errors.New("POPO verification socket requires a private directory")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("POPO verification socket path is occupied")
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_ = conn.Close()
			return errors.New("POPO verification socket already has a listener")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return err
	}
	b.start(listener)
	return nil
}

func (b *DockerPopoVerificationBridge) start(listener net.Listener) {
	b.listener = listener
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			b.attach(conn)
		}
	}()
}

func (b *DockerPopoVerificationBridge) attach(conn net.Conn) {
	decoder := json.NewDecoder(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var hello dockerPopoHello
	if err := decoder.Decode(&hello); err != nil || hello.Version != dockerPopoProtocolVersion {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	worker := &dockerPopoWorker{conn: conn, encoder: json.NewEncoder(conn), decoder: decoder, slot: make(chan struct{}, 1)}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = conn.Close()
		return
	}
	previous := b.worker
	b.worker = worker
	b.mu.Unlock()
	if previous != nil {
		_ = previous.conn.Close()
	}
	slog.Info("Windows POPO verification sender connected through local Docker")
}

func (b *DockerPopoVerificationBridge) Close() {
	b.mu.Lock()
	b.closed = true
	worker := b.worker
	b.worker = nil
	b.mu.Unlock()
	if b.listener != nil {
		_ = b.listener.Close()
	}
	if worker != nil {
		_ = worker.conn.Close()
	}
}

func (b *DockerPopoVerificationBridge) SendVerificationCode(ctx context.Context, email, code string) error {
	b.mu.Lock()
	worker := b.worker
	b.mu.Unlock()
	if worker == nil {
		return errors.New("Windows POPO verification sender is offline")
	}

	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	select {
	case worker.slot <- struct{}{}:
		defer func() { <-worker.slot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	stop := context.AfterFunc(ctx, func() { _ = worker.conn.Close() })
	defer stop()
	if err := worker.conn.SetDeadline(deadline); err != nil {
		return b.disconnect(worker, err)
	}
	if err := worker.encoder.Encode(popoVerificationRelayRequest{Email: email, Code: code}); err != nil {
		return b.disconnect(worker, err)
	}
	var receipt dockerPopoReceipt
	if err := worker.decoder.Decode(&receipt); err != nil {
		return b.disconnect(worker, err)
	}
	if receipt.Accepted == nil {
		return b.disconnect(worker, errors.New("POPO receipt missing acceptance status"))
	}
	if !*receipt.Accepted {
		return errors.New("Windows POPO CLI did not confirm submission")
	}
	return nil
}

func (b *DockerPopoVerificationBridge) disconnect(worker *dockerPopoWorker, err error) error {
	_ = worker.conn.Close()
	b.mu.Lock()
	if b.worker == worker {
		b.worker = nil
	}
	b.mu.Unlock()
	return fmt.Errorf("Windows POPO verification connection lost: %w", err)
}

// RunDockerPopoVerificationWorker runs under the Windows user's existing login.
// Its wire output contains only the protocol handshake and submission receipts.
func RunDockerPopoVerificationWorker(ctx context.Context, input io.Reader, output io.Writer, sender PopoVerificationCodeSender) error {
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(dockerPopoHello{Version: dockerPopoProtocolVersion}); err != nil {
		return err
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	for {
		var request popoVerificationRelayRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !accountPattern.MatchString(request.Email) || !popoVerificationCodePattern.MatchString(request.Code) {
			return errors.New("invalid POPO verification request")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err := sender.SendVerificationCode(ctx, request.Email, request.Code)
		accepted := err == nil
		if err != nil {
			slog.Warn("Windows POPO verification send failed", "error", strings.ReplaceAll(err.Error(), request.Code, "[redacted]"))
		}
		if err := encoder.Encode(dockerPopoReceipt{Accepted: &accepted}); err != nil {
			return err
		}
	}
}
