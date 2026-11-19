package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPopoVerificationRelaySendsThroughLocalCLI(t *testing.T) {
	token := strings.Repeat("a", 64)
	var sends atomic.Int32
	cli := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		sends.Add(1)
		if args[1] != "message_send_p2p" || argValue(args, "receiver") != "user@example.test" {
			t.Errorf("unexpected POPO invocation %v", args)
		}
		message, err := os.ReadFile(strings.TrimPrefix(argValue(args, "message"), "@text:"))
		if err != nil {
			return nil, err
		}
		if contents := string(message); !strings.Contains(contents, "Multica 验证码：123456") ||
			!strings.Contains(contents, "用于注册或登录 Multica") || !strings.Contains(contents, "10 分钟内有效") {
			t.Errorf("unexpected POPO verification message %q", contents)
		}
		return popoResponse(map[string]any{}), nil
	}}
	handler, err := NewPopoVerificationRelayHandler(token, cli)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewTLSServer(handler)
	t.Cleanup(relay.Close)
	sender := NewPopoVerificationRelaySender(relay.URL, token).(*popoVerificationRelaySender)
	sender.client.Transport = relay.Client().Transport
	if err := sender.SendVerificationCode(t.Context(), "user@example.test", "123456"); err != nil {
		t.Fatal(err)
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("POPO CLI invocations = %d, want 1", got)
	}
}

func TestPopoVerificationRelayRejectsUnauthorizedOrInvalidRequests(t *testing.T) {
	token := strings.Repeat("a", 64)
	cli := Popo{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid request invoked the POPO CLI")
		return nil, nil
	}}
	handler, err := NewPopoVerificationRelayHandler(token, cli)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		auth   string
		body   string
		status int
	}{
		{"missing credentials", "", `{"email":"user@example.test","code":"123456"}`, http.StatusUnauthorized},
		{"wrong credentials", "Bearer " + strings.Repeat("b", 64), `{"email":"user@example.test","code":"123456"}`, http.StatusUnauthorized},
		{"invalid code", "Bearer " + token, `{"email":"user@example.test","code":"12345"}`, http.StatusBadRequest},
		{"invalid receiver", "Bearer " + token, `{"email":"12345","code":"123456"}`, http.StatusBadRequest},
		{"extra command", "Bearer " + token, `{"email":"user@example.test","code":"123456","command":"custom"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://relay.test"+popoVerificationRelayPath, strings.NewReader(tc.body))
			request.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("HTTP %d, want %d", response.Code, tc.status)
			}
		})
	}
}

func TestPopoVerificationRelayHandlerRejectsNilSender(t *testing.T) {
	if _, err := NewPopoVerificationRelayHandler(strings.Repeat("a", 64), nil); err == nil {
		t.Fatal("nil sender accepted by verification relay handler")
	}
}
