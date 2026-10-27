package popo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

const (
	ConversationChat        = "chat"
	ConversationRequirement = "requirement"
)

// MemberConversation stores a preference and pointers, never a copy of server
// requirement state. Every read still checks the current participant permissions.
type MemberConversation struct {
	Mode             string `json:"mode"`
	RequirementID    string `json:"requirement_id,omitempty"`
	DraftOperationID string `json:"draft_operation_id,omitempty"`
}

func ConversationScopeKey(chatID string, chatType channel.ChatType) string {
	return string(chatType) + ":" + chatID
}

func ReadMemberConversation(raw []byte, scopeKey string) MemberConversation {
	var config struct {
		Conversations map[string]MemberConversation `json:"popo_conversations"`
		DefaultMode   string                        `json:"popo_default_mode"`
	}
	_ = json.Unmarshal(raw, &config)
	conversation, selected := config.Conversations[scopeKey]
	if !selected && config.DefaultMode == ConversationRequirement {
		conversation.Mode = ConversationRequirement
	}
	if conversation.Mode != ConversationRequirement {
		conversation.Mode = ConversationChat
	}
	return conversation
}

const MemberCommandHelp = `个人会话命令（只影响你在此机器人、此对话中的选择）：
/mode — 查看当前模式
/mode chat 或 /chat — 切换普通聊天
/mode requirement 或 /requirement — 切换需求流程
/requirement new [模板链接] <想法> — 发起独立需求
/requirement status [需求 ID] — 查看需求阶段和下一步
/requirement list — 列出你参与的近期需求
/requirement open <需求 ID> — 选择需求并打开对应页面
/requirement resolve <操作 ID> <新单链接> — 核对未确认的来源草稿
/requirement help 或 /help — 查看帮助
/new <消息> — 在当前模式中新建聊天或需求
需求的澄清、批准、验收和取消请在 Multica 中操作。`

type MemberCommand struct {
	Action string
	Mode   string
	Target string
	URL    string
	Body   string
}

func RequirementStateLabel(state string) string {
	if label, ok := map[string]string{"intake_pending": "待开始", "clarifying": "澄清中", "awaiting_approval": "待批准", "executing": "执行中", "blocked": "已阻塞", "in_review": "待验收", "done": "已完成", "cancelled": "已取消"}[state]; ok {
		return label
	}
	return "状态未知"
}

func RequirementNextStep(state string) string {
	switch state {
	case "intake_pending":
		return "下一步：打开 Multica Chat 开始澄清。"
	case "clarifying":
		return "下一步：在 Multica Chat 补充需求；当前为只读 Plan，尚未执行。"
	case "awaiting_approval":
		return "下一步：由 approver 在 Multica Chat 审查并批准候选。"
	case "executing":
		return "下一步：在执行任务中查看进度或补充信息。"
	case "blocked":
		return "下一步：指定参与者在执行任务的问题评论线程回复。"
	case "in_review":
		return "下一步：在 Multica 审阅交付结果，验收通过或打回修改。"
	case "done":
		return "需求已完成；来源回写和通知结果请查看同步状态。"
	case "cancelled":
		return "需求已取消；可用 /requirement new <想法> 发起独立需求。"
	default:
		return "请在 Multica 查看需求的最新状态。"
	}
}

// ParseMemberCommand reserves controls before free-form intake. Unknown
// subcommands are usage errors and can never create an external draft.
func ParseMemberCommand(text string) (MemberCommand, bool, error) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return MemberCommand{}, false, nil
	}
	invalid := func() (MemberCommand, bool, error) {
		return MemberCommand{}, true, fmt.Errorf("命令或参数不正确。\n%s", MemberCommandHelp)
	}
	switch parts[0] {
	case "/help":
		if len(parts) != 1 {
			return invalid()
		}
		return MemberCommand{Action: "help"}, true, nil
	case "/chat":
		if len(parts) != 1 {
			return invalid()
		}
		return MemberCommand{Action: "mode", Mode: ConversationChat}, true, nil
	case "/mode":
		if len(parts) == 1 {
			return MemberCommand{Action: "status_mode"}, true, nil
		}
		if len(parts) != 2 || (parts[1] != ConversationChat && parts[1] != ConversationRequirement) {
			return invalid()
		}
		return MemberCommand{Action: "mode", Mode: parts[1]}, true, nil
	case "/requirement", "/需求":
	default:
		return MemberCommand{}, false, nil
	}
	if len(parts) == 1 {
		return MemberCommand{Action: "mode", Mode: ConversationRequirement}, true, nil
	}
	switch parts[1] {
	case "help", "帮助", "list", "列表":
		if len(parts) != 2 {
			return invalid()
		}
		action := "help"
		if parts[1] == "list" || parts[1] == "列表" {
			action = "list"
		}
		return MemberCommand{Action: action}, true, nil
	case "status", "状态":
		if len(parts) > 3 {
			return invalid()
		}
		command := MemberCommand{Action: "status"}
		if len(parts) == 3 {
			command.Target = parts[2]
		}
		return command, true, nil
	case "open", "打开":
		if len(parts) != 3 {
			return invalid()
		}
		return MemberCommand{Action: "open", Target: parts[2]}, true, nil
	case "resolve", "核对":
		if len(parts) != 4 {
			return invalid()
		}
		return MemberCommand{Action: "resolve", Target: parts[2], URL: parts[3]}, true, nil
	case "new", "新建":
		if len(parts) < 3 {
			return invalid()
		}
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), parts[0])), parts[1]))
		command := MemberCommand{Action: "new"}
		if strings.HasPrefix(parts[2], "https://") {
			command.URL = parts[2]
			body = strings.TrimSpace(strings.TrimPrefix(body, parts[2]))
		}
		if body == "" {
			return invalid()
		}
		command.Body = body
		return command, true, nil
	default:
		return invalid()
	}
}
