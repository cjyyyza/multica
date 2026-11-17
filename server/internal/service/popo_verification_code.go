package service

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/notify"
)

type popoP2PSender interface {
	SendP2P(ctx context.Context, receiver, message string) (notify.Receipt, error)
}

type PopoVerificationCodeService struct {
	cli popoP2PSender
}

func NewPopoVerificationCodeServiceFromEnv() *PopoVerificationCodeService {
	bin := strings.TrimSpace(os.Getenv("MULTICA_POPO_CLI"))
	if bin == "" {
		return nil
	}
	return &PopoVerificationCodeService{cli: notify.Popo{Bin: bin}}
}

func (s *PopoVerificationCodeService) SendVerificationCode(ctx context.Context, email, code string) error {
	_, err := s.cli.SendP2P(ctx, email, fmt.Sprintf("Multica 验证码：%s\n用于注册或登录 Multica，10 分钟内有效。如果不是你本人发起，请忽略此消息。", code))
	if err != nil {
		return fmt.Errorf("send verification code with popo-cli: %w", err)
	}
	return nil
}
