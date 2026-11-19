package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/notify"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeTestSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "relay.crt")
	keyFile := filepath.Join(dir, "relay.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func writeFakePopoExecutableDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "popo-cli"
	if runtime.GOOS == "windows" {
		name = "popo-cli.exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPopoVerificationRelayCmdValidationAndGracefulShutdown(t *testing.T) {
	certFile, keyFile := writeTestSelfSignedCert(t)
	validToken := strings.Repeat("a", 64)

	t.Run("rejects missing popo-cli at startup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv(notify.PopoVerificationRelayTokenEnv, validToken)
		cmd := newPopoVerificationRelayCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--cert", certFile, "--key", keyFile})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "find popo-cli") {
			t.Fatalf("err = %v, want missing popo-cli error", err)
		}
	})

	t.Run("rejects wrapper without node on PATH at startup", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows shim resolution only applies to .cmd/.ps1 wrappers")
		}
		dir := t.TempDir()
		script := filepath.Join(dir, "node_modules", "@popo", "cli", "bin", "popo.js")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("console.log('ok')"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "popo-cli.cmd"), []byte("@echo off\r\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		t.Setenv(notify.PopoVerificationRelayTokenEnv, validToken)
		cmd := newPopoVerificationRelayCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--cert", certFile, "--key", keyFile})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "find Node.js") {
			t.Fatalf("err = %v, want missing Node.js error", err)
		}
	})

	t.Run("rejects invalid relay token before binding", func(t *testing.T) {
		t.Setenv("PATH", writeFakePopoExecutableDir(t))
		t.Setenv(notify.PopoVerificationRelayTokenEnv, "too-short")
		cmd := newPopoVerificationRelayCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--cert", certFile, "--key", keyFile})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), notify.PopoVerificationRelayTokenEnv) {
			t.Fatalf("err = %v, want invalid token error", err)
		}
	})

	t.Run("serves HTTPS and shuts down cleanly on context cancellation", func(t *testing.T) {
		t.Setenv("PATH", writeFakePopoExecutableDir(t))
		t.Setenv(notify.PopoVerificationRelayTokenEnv, validToken)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var errOut syncBuffer
		cmd := newPopoVerificationRelayCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(&errOut)
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--cert", certFile, "--key", keyFile})

		done := make(chan error, 1)
		go func() {
			done <- cmd.ExecuteContext(ctx)
		}()

		var relayURL string
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			text := errOut.String()
			if idx := strings.Index(text, "https://"); idx >= 0 {
				line := text[idx:]
				if nl := strings.IndexByte(line, '\n'); nl >= 0 {
					relayURL = strings.TrimSpace(line[:nl])
					break
				}
			}
			select {
			case err := <-done:
				t.Fatalf("relay exited before listening: %v", err)
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}
		if relayURL == "" {
			cancel()
			t.Fatalf("timed out waiting for relay startup; stderr=%q", errOut.String())
		}

		client := &http.Client{
			Timeout:   3 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}
		resp, err := client.Post(relayURL+"/v1/verification-code", "application/json", strings.NewReader(`{}`))
		if err != nil {
			cancel()
			t.Fatalf("HTTPS request to relay failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			cancel()
			t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("graceful shutdown returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("relay did not exit cleanly after context cancellation")
		}
	})
}
