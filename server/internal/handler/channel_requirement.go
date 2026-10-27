package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) handleRequirementIntake(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage, startChat bool) (engine.Result, bool, error) {
	if msg.Source.ChannelType != popo.TypePopo {
		return engine.Result{}, false, nil
	}
	body := strings.TrimSpace(msg.CommandText)
	parts := strings.Fields(body)
	explicit := len(parts) > 0 && (parts[0] == "/requirement" || parts[0] == "/需求") && !startChat
	installation, ok := inst.Platform.(db.ChannelInstallation)
	templateURL := ""
	if ok {
		templateURL = popo.DecodePublicConfig(installation.Config).RequirementTemplateURL
	}
	if !explicit && (templateURL == "" || strings.HasPrefix(body, "/yixiezuo ") || body == "/yixiezuo") {
		return engine.Result{}, false, nil
	}
	result := engine.Result{Outcome: engine.OutcomeIssueFollow, InstallationID: inst.ID, Sender: msg.Source.SenderID}
	if !inst.ID.Valid || !identity.UserID.Valid || strings.TrimSpace(msg.MessageID) == "" || strings.TrimSpace(msg.Source.ChatID) == "" {
		result.ReplyText = "需要已绑定的工作区成员和有效消息标识才能发起需求。"
		return result, true, nil
	}
	key := sourceCommandKey(inst, identity.UserID, msg.MessageID)
	result.ReplyKey = "requirement_ack:" + key
	scope := &yixiezuo.ChannelScope{InstallationID: uuidToString(inst.ID), ChatID: msg.Source.ChatID, ChatType: string(msg.Source.ChatType)}
	finish := func(row db.YixiezuoOperation, err error) (engine.Result, bool, error) {
		var action *yixiezuoActionError
		if errors.As(err, &action) {
			result.ReplyText = action.message
			return result, true, nil
		}
		if err != nil {
			return result, true, err
		}
		result.ReplyText = h.requirementDraftReply(ctx, row)
		return result, true, nil
	}
	if explicit {
		body = strings.TrimSpace(strings.TrimPrefix(body, parts[0]))
		if len(parts) == 4 && (parts[1] == "resolve" || parts[1] == "核对") {
			id, err := sourceUUID(parts[2], "draft operation ID")
			if err != nil {
				return finish(db.YixiezuoOperation{}, err)
			}
			row, err := h.queueRequirementDraftReconcile(ctx, inst.WorkspaceID, identity.UserID, id, parts[3], "draft-reconcile:"+key, scope)
			if err != nil {
				return finish(row, err)
			}
			result.ReplyText = "正在只读核对新单据，核对成功后会发送原需求的 Multica Chat 链接。"
			return result, true, nil
		}
		if len(parts) > 1 && strings.HasPrefix(parts[1], "https://") {
			templateURL = parts[1]
			body = strings.TrimSpace(strings.TrimPrefix(body, parts[1]))
		}
	}
	if body == "" || templateURL == "" {
		result.ReplyText = "发送 /requirement <易协作模板单链接> <需求想法>。已配置需求入口的机器人可直接接收想法，用 /new <想法> 发起独立新需求。澄清和批准在 Multica Chat 中进行。"
		return result, true, nil
	}
	if strings.HasPrefix(body, "/") && !explicit && !startChat {
		result.ReplyText = "请在 Multica Chat 中继续澄清、批准和回复问题；用 /new <想法> 发起独立新需求。"
		return result, true, nil
	}
	// The inbound request key wins over the current conversation pointer on replay.
	if saved, exists, err := existingSourceRequest(ctx, h.Queries, inst.WorkspaceID, identity.UserID, "draft:"+key); err != nil {
		return result, true, err
	} else if exists {
		var payload yixiezuo.OperationPayload
		if jsonErr := json.Unmarshal(saved.Payload, &payload); jsonErr != nil || payload.Draft == nil || payload.Draft.Body != body || !sourceScopeMatches(payload.Channel, scope) {
			return finish(saved, sourceError(409, "此消息已用于另一个需求，请重新发送新消息。"))
		}
		row, err := h.settleRequirementDraft(ctx, saved)
		return finish(row, err)
	}
	if !startChat && !explicit {
		current, err := h.Queries.LatestYixiezuoDraftByChannel(ctx, db.LatestYixiezuoDraftByChannelParams{WorkspaceID: inst.WorkspaceID, RequestedBy: identity.UserID, InstallationID: uuidToString(inst.ID), ChatID: msg.Source.ChatID})
		if err == nil && current.State != "failed" {
			row, err := h.settleRequirementDraft(ctx, current)
			return finish(row, err)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, true, err
		}
	}
	title := []rune(strings.SplitN(body, "\n", 2)[0])
	if len(title) > 120 {
		title = title[:120]
	}
	row, err := h.queueRequirementDraft(ctx, inst.WorkspaceID, identity.UserID, requirementDraftRequest{TemplateURL: templateURL, Title: string(title), Body: body, AgentID: uuidToString(inst.AgentID)}, key, scope)
	return finish(row, err)
}
