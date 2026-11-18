package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/notify"
)

type fakePopoP2PSender struct {
	receiver string
	message  string
}

func (f *fakePopoP2PSender) SendP2P(_ context.Context, receiver, message string) (notify.Receipt, error) {
	f.receiver = receiver
	f.message = message
	return notify.Receipt{MessageID: "message-1", Receiver: receiver}, nil
}

func TestPopoVerificationCodeService(t *testing.T) {
	cli := &fakePopoP2PSender{}
	service := &PopoVerificationCodeService{cli: cli}
	if err := service.SendVerificationCode(context.Background(), "user@corp.netease.com", "123456"); err != nil {
		t.Fatal(err)
	}
	if cli.receiver != "user@corp.netease.com" ||
		!strings.Contains(cli.message, "Multica 验证码：123456") ||
		!strings.Contains(cli.message, "用于注册或登录 Multica") ||
		!strings.Contains(cli.message, "10 分钟内有效") {
		t.Fatalf("unexpected POPO delivery: %+v", cli)
	}
}

func TestPopoVerificationCodeServiceFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		unset   bool
		wantBin string
	}{
		{name: "unset defaults to enabled", unset: true, wantBin: "popo-cli"},
		{name: "empty defaults to enabled", value: "", wantBin: "popo-cli"},
		{name: "true enables POPO", value: "true", wantBin: "popo-cli"},
		{name: "false disables POPO", value: "false"},
		{name: "false is case insensitive", value: " FALSE "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MULTICA_POPO_CLI", tc.value)
			if tc.unset {
				if err := os.Unsetenv("MULTICA_POPO_CLI"); err != nil {
					t.Fatal(err)
				}
			}

			var sender PopoVerificationCodeSender = NewPopoVerificationCodeServiceFromEnv()
			if tc.wantBin == "" {
				if sender != nil {
					t.Fatal("false must disable POPO and preserve email delivery")
				}
				return
			}
			service, ok := sender.(*PopoVerificationCodeService)
			if !ok {
				t.Fatalf("expected enabled POPO service, got %T", sender)
			}
			cli, ok := service.cli.(notify.Popo)
			if !ok {
				t.Fatalf("expected POPO CLI sender, got %T", service.cli)
			}
			if cli.Bin != tc.wantBin {
				t.Fatalf("CLI path = %q, want %q", cli.Bin, tc.wantBin)
			}
		})
	}
}
