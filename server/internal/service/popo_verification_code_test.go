package service

import (
	"context"
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
	t.Setenv("MULTICA_POPO_CLI", "")
	if service := NewPopoVerificationCodeServiceFromEnv(); service != nil {
		t.Fatal("empty CLI path must preserve email delivery")
	}
	var sender PopoVerificationCodeSender = NewPopoVerificationCodeServiceFromEnv()
	if sender != nil {
		t.Fatal("empty CLI path must stay nil when stored in a sender interface")
	}
	t.Setenv("MULTICA_POPO_CLI", "popo-cli")
	if service := NewPopoVerificationCodeServiceFromEnv(); service == nil {
		t.Fatal("configured CLI path must enable POPO delivery")
	}
}
