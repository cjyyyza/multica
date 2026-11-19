package service

import (
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/notify"
)

// NewPopoVerificationCodeServiceFromEnv returns an interface value so a
// disabled service yields an untyped nil.
func NewPopoVerificationCodeServiceFromEnv() notify.PopoVerificationCodeSender {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MULTICA_POPO_CLI")), "false") {
		return nil
	}
	relayURL := strings.TrimSpace(os.Getenv(notify.PopoVerificationRelayURLEnv))
	relayToken := strings.TrimSpace(os.Getenv(notify.PopoVerificationRelayTokenEnv))
	if relayURL != "" || relayToken != "" {
		return notify.NewPopoVerificationRelaySender(relayURL, relayToken)
	}
	return notify.Popo{Bin: "popo-cli"}
}
