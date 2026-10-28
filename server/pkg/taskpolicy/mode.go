package taskpolicy

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ModeExecute = "execute"
	ModePlan    = "plan"

	HeaderTaskMode       = "X-Task-Mode"
	HeaderRequirementID  = "X-Requirement-ID"
	HeaderChatSessionID  = "X-Task-Chat-Session-ID"
	PlanModeToolDenied   = "plan_mode_tool_denied"
	PlanModeUnavailable  = "plan_mode_unavailable"
	PlanPermissionClaude = "claude-native-plan-v1"
	PlanPermissionDsh    = "dsh-native-plan-v1"
)

func PlanPermission(provider string) string {
	switch provider {
	case "claude":
		return PlanPermissionClaude
	case "dsh":
		return PlanPermissionDsh
	default:
		return ""
	}
}

var ErrInvalidMode = errors.New("invalid task mode")

func NormalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeExecute:
		return ModeExecute, nil
	case ModePlan:
		return ModePlan, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidMode, mode)
	}
}

func IsPlan(mode string) bool {
	normalized, err := NormalizeMode(mode)
	return err == nil && normalized == ModePlan
}

func AllowsExternalTools(mode string) bool {
	normalized, err := NormalizeMode(mode)
	return err == nil && normalized == ModeExecute
}
