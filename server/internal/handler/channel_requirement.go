package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) handleRequirementIntake(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage, startChat bool) (engine.Result, bool, error) {
	if msg.Source.ChannelType != popo.TypePopo {
		return engine.Result{}, false, nil
	}
	command, explicit, parseErr := popo.ParseMemberCommand(msg.CommandText)
	// /new has already been consumed by the shared parser. Its remainder is data.
	if startChat {
		explicit, parseErr = false, nil
	}
	binding, err := h.Queries.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{InstallationID: inst.ID, ChannelUserID: msg.Source.SenderID})
	if err != nil {
		return engine.Result{}, false, err
	}
	if binding.WorkspaceID != inst.WorkspaceID || binding.MulticaUserID != identity.UserID {
		return engine.Result{}, false, fmt.Errorf("POPO conversation identity does not match its binding")
	}
	conversation := popo.ReadMemberConversation(binding.Config, popo.ConversationScopeKey(msg.Source.ChatID, msg.Source.ChatType))
	result := engine.Result{Outcome: engine.OutcomeIssueFollow, InstallationID: inst.ID, Sender: msg.Source.SenderID, ReplyToSender: true, ReplyKey: "requirement_ack:" + sourceCommandKey(inst, identity.UserID, msg.MessageID)}
	finish := func(reply string, err error) (engine.Result, bool, error) {
		var sourceErr *yixiezuoActionError
		var requirementErr *service.RequirementError
		if errors.As(err, &sourceErr) {
			reply, err = sourceErr.message, nil
		} else if errors.As(err, &requirementErr) {
			reply, err = requirementErr.Message, nil
		}
		result.ReplyText = reply
		return result, true, err
	}
	if msg.CommandReplay && (!explicit || command.Action == "new") {
		row, exists, err := existingSourceRequest(ctx, h.Queries, inst.WorkspaceID, identity.UserID, "draft:"+sourceCommandKey(inst, identity.UserID, msg.MessageID))
		if err != nil {
			return finish("", err)
		}
		if exists && row.Kind == "draft" {
			if conversation.RequirementID == "" && conversation.DraftOperationID == "" {
				conversation.Mode = popo.ConversationRequirement
				conversation.DraftOperationID = uuidToString(row.ID)
				if err := h.savePopoConversation(ctx, inst, identity, msg, conversation); err != nil {
					return finish("", err)
				}
			}
			row, err = h.settleRequirementDraft(ctx, row)
			return finish("需求流程 · 原消息的来源草稿\n"+h.requirementDraftReply(ctx, row), err)
		}
		// An ordinary /new or message from an earlier mode owns no draft.
		// The shared router will drop it without running the chat pipeline again.
		return engine.Result{}, false, nil
	}
	if msg.CommandReplay && explicit && (command.Action == "mode" || command.Action == "open") {
		_, err := h.Queries.GetChannelInboundWrite(ctx, db.GetChannelInboundWriteParams{InstallationID: inst.ID, MessageID: msg.MessageID, Kind: "popo_conversation"})
		if errors.Is(err, pgx.ErrNoRows) {
			return finish("此消息已处理。若要切换个人会话，请发送新的命令。", nil)
		}
		if err != nil {
			return finish("", err)
		}
	}
	if parseErr != nil {
		return finish(parseErr.Error(), nil)
	}
	if explicit {
		switch command.Action {
		case "help":
			return finish(popoConversationModeText(conversation.Mode)+"\n"+popo.MemberCommandHelp, nil)
		case "status_mode":
			return finish(popoConversationModeText(conversation.Mode)+"\n/requirement help 查看命令。", nil)
		case "mode":
			if command.Mode == popo.ConversationRequirement && !featureflags.RequirementFlowEnabled(ctx, h.FeatureFlags) {
				return finish("需求流程尚未启用。当前会话模式未改变。", nil)
			}
			conversation.Mode = command.Mode
			if err := h.savePopoConversation(ctx, inst, identity, msg, conversation); err != nil {
				return finish("", err)
			}
			// Re-read after replay: an older duplicate must not undo a newer selection.
			conversation, err = h.readPopoConversation(ctx, inst, identity, msg)
			if err != nil {
				return finish("", err)
			}
			if conversation.Mode == popo.ConversationChat {
				return finish(popoConversationModeText(conversation.Mode)+"\n下一条普通消息进入你自己的聊天上下文。/new <消息> 开新聊天；/requirement 切回需求流程。", nil)
			}
			return h.popoCurrentRequirementReply(ctx, inst, identity, msg, conversation, finish)
		case "list":
			rows, err := h.Queries.ListRequirementsForPopoMember(ctx, db.ListRequirementsForPopoMemberParams{WorkspaceID: inst.WorkspaceID, AgentID: inst.AgentID, UserID: identity.UserID})
			if err != nil {
				return finish("", err)
			}
			lines := []string{popoConversationModeText(conversation.Mode), "你参与的近期需求："}
			for _, row := range rows {
				detail, err := h.requirementService().GetRequirement(ctx, service.RequirementActor{WorkspaceID: inst.WorkspaceID, UserID: identity.UserID}, row.ID)
				if err != nil {
					return finish("", err)
				}
				var source struct{ Title string }
				_ = json.Unmarshal(row.SourceSnapshot, &source)
				lines = append(lines, fmt.Sprintf("%s · %s · %s", uuidToString(row.ID), popo.RequirementStateLabel(detail.State), source.Title))
			}
			if len(rows) == 0 {
				lines = append(lines, "暂无可访问需求。/requirement new <想法> 发起需求。")
			} else {
				lines = append(lines, "/requirement open <需求 ID> 选择需求。")
			}
			return finish(strings.Join(lines, "\n"), nil)
		case "status", "open":
			if command.Target == "" {
				return h.popoCurrentRequirementReply(ctx, inst, identity, msg, conversation, finish)
			}
			id, err := sourceUUID(command.Target, "requirement ID")
			if err != nil {
				return finish("", err)
			}
			detail, err := h.requirementService().GetRequirement(ctx, service.RequirementActor{WorkspaceID: inst.WorkspaceID, UserID: identity.UserID}, id)
			if err != nil || detail.AgentID == nil || *detail.AgentID != uuidToString(inst.AgentID) {
				return finish("找不到你在此机器人下有权访问的需求。", nil)
			}
			if command.Action == "open" {
				conversation.Mode, conversation.RequirementID, conversation.DraftOperationID = popo.ConversationRequirement, detail.ID, ""
				if err := h.savePopoConversation(ctx, inst, identity, msg, conversation); err != nil {
					return finish("", err)
				}
				conversation, err = h.readPopoConversation(ctx, inst, identity, msg)
				if err != nil {
					return finish("", err)
				}
				return h.popoCurrentRequirementReply(ctx, inst, identity, msg, conversation, finish)
			}
			return finish(h.popoRequirementStatus(ctx, inst, detail, conversation.Mode), nil)
		case "resolve":
			id, err := sourceUUID(command.Target, "draft operation ID")
			if err != nil {
				return finish("", err)
			}
			row, err := h.queueRequirementDraftReconcile(ctx, inst.WorkspaceID, identity.UserID, id, command.URL, "draft-reconcile:"+sourceCommandKey(inst, identity.UserID, msg.MessageID), popoRequirementScope(inst, identity.UserID, msg))
			if err != nil {
				return finish("", err)
			}
			return finish("需求流程 · 正在只读核对来源草稿\n"+h.requirementDraftReply(ctx, row), nil)
		case "new":
			return h.newPopoRequirement(ctx, inst, identity, msg, conversation, command.Body, command.URL, finish)
		}
	}
	if conversation.Mode != popo.ConversationRequirement {
		return engine.Result{}, false, nil
	}
	body := strings.TrimSpace(msg.CommandText)
	// Existing issue/source commands keep their own authorization and semantics.
	if _, ok := engine.ParseIssueCommand(body); ok {
		return engine.Result{}, false, nil
	}
	if _, ok := engine.ParseFollowCommand(body); ok {
		return engine.Result{}, false, nil
	}
	if strings.HasPrefix(body, "/yixiezuo") {
		return engine.Result{}, false, nil
	}
	if !startChat && msg.ReplyTo != nil {
		target, err := h.LookupQuote(ctx, engine.QuoteLookupInput{InstallationID: inst.ID, WorkspaceID: inst.WorkspaceID, ChatID: msg.Source.ChatID, MessageID: msg.ReplyTo.MessageID})
		if err == nil && target.IssueID.Valid {
			return engine.Result{}, false, nil
		}
	}
	if !startChat && strings.HasPrefix(body, "/") {
		return finish("需求流程中的控制命令请用 /requirement help 查看。批准和验收在 Multica 中确认。", nil)
	}
	if body == "" {
		return h.popoCurrentRequirementReply(ctx, inst, identity, msg, conversation, finish)
	}
	if startChat || (conversation.RequirementID == "" && conversation.DraftOperationID == "") {
		return h.newPopoRequirement(ctx, inst, identity, msg, conversation, body, "", finish)
	}
	reply, _, err := h.popoCurrentRequirementReply(ctx, inst, identity, msg, conversation, finish)
	if err == nil {
		reply.ReplyText += "\n本条消息未追加到需求；请打开对应页面继续补充。/requirement new <想法> 发起独立需求，/chat 切换普通聊天。"
	}
	return reply, true, err
}

type popoRequirementReply func(string, error) (engine.Result, bool, error)

func popoRequirementScope(inst engine.ResolvedInstallation, actor pgtype.UUID, msg channel.InboundMessage) *yixiezuo.ChannelScope {
	return &yixiezuo.ChannelScope{InstallationID: uuidToString(inst.ID), ChatID: msg.Source.ChatID, ChatType: string(msg.Source.ChatType), SessionKey: popo.SessionKey(msg, actor)}
}

func popoConversationModeText(mode string) string {
	if mode == popo.ConversationRequirement {
		return "你的当前模式：需求流程"
	}
	return "你的当前模式：普通聊天"
}

func (h *Handler) readPopoConversation(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage) (popo.MemberConversation, error) {
	binding, err := h.Queries.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{InstallationID: inst.ID, ChannelUserID: msg.Source.SenderID})
	if err != nil {
		return popo.MemberConversation{}, err
	}
	if binding.WorkspaceID != inst.WorkspaceID || binding.MulticaUserID != identity.UserID {
		return popo.MemberConversation{}, errors.New("POPO member binding changed")
	}
	return popo.ReadMemberConversation(binding.Config, popo.ConversationScopeKey(msg.Source.ChatID, msg.Source.ChatType)), nil
}

func (h *Handler) savePopoConversation(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage, conversation popo.MemberConversation) error {
	if msg.MessageID == "" || msg.Source.ChatID == "" {
		return errors.New("a durable POPO message and conversation identity are required")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	_, err = q.ClaimPopoConversationCommand(ctx, db.ClaimPopoConversationCommandParams{WorkspaceID: inst.WorkspaceID, InstallationID: inst.ID, MessageID: msg.MessageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := json.Marshal(conversation)
	if err != nil {
		return err
	}
	_, err = q.UpdatePopoMemberConversation(ctx, db.UpdatePopoMemberConversationParams{WorkspaceID: inst.WorkspaceID, InstallationID: inst.ID, UserID: identity.UserID, ChannelUserID: msg.Source.SenderID, ScopeKey: popo.ConversationScopeKey(msg.Source.ChatID, msg.Source.ChatType), Conversation: raw})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) newPopoRequirement(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage, conversation popo.MemberConversation, body, override string, finish popoRequirementReply) (engine.Result, bool, error) {
	configured := ""
	if installation, ok := inst.Platform.(db.ChannelInstallation); ok {
		configured = popo.DecodePublicConfig(installation.Config).RequirementTemplateURL
	}
	title := []rune(strings.SplitN(body, "\n", 2)[0])
	if len(title) > 120 {
		title = title[:120]
	}
	row, err := h.queueRequirementDraft(ctx, inst.WorkspaceID, identity.UserID, requirementDraftRequest{TemplateURL: yixiezuo.ResolveRequirementTemplateURL(configured, override), Title: string(title), Body: body, AgentID: uuidToString(inst.AgentID)}, sourceCommandKey(inst, identity.UserID, msg.MessageID), popoRequirementScope(inst, identity.UserID, msg))
	if err != nil {
		return finish("", err)
	}
	conversation.Mode, conversation.RequirementID, conversation.DraftOperationID = popo.ConversationRequirement, "", uuidToString(row.ID)
	if err := h.savePopoConversation(ctx, inst, identity, msg, conversation); err != nil {
		return finish("", err)
	}
	return finish("需求流程 · 来源草稿\n"+h.requirementDraftReply(ctx, row), nil)
}

func (h *Handler) popoCurrentRequirementReply(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage, conversation popo.MemberConversation, finish popoRequirementReply) (engine.Result, bool, error) {
	if conversation.RequirementID == "" && conversation.DraftOperationID != "" {
		id, err := sourceUUID(conversation.DraftOperationID, "draft operation ID")
		if err != nil {
			return finish("", err)
		}
		row, err := h.Queries.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{WorkspaceID: inst.WorkspaceID, RequestedBy: identity.UserID, ID: id})
		if err != nil {
			return finish("当前草稿已不可访问，请用 /requirement list 重新选择。", nil)
		}
		var payload yixiezuo.OperationPayload
		if json.Unmarshal(row.Payload, &payload) != nil || payload.Draft == nil || payload.Draft.AgentID != uuidToString(inst.AgentID) || !sourceScopeMatches(payload.Channel, popoRequirementScope(inst, identity.UserID, msg)) {
			return finish("当前草稿不属于此个人会话，请重新选择。", nil)
		}
		row, err = h.settleRequirementDraft(ctx, row)
		if err != nil {
			return finish("", err)
		}
		if !row.RequirementID.Valid {
			return finish(popoConversationModeText(conversation.Mode)+"\n"+h.requirementDraftReply(ctx, row), nil)
		}
		conversation.RequirementID = uuidToString(row.RequirementID)
	}
	if conversation.RequirementID == "" {
		return finish(popoConversationModeText(conversation.Mode)+"\n当前未选择需求。/requirement new <想法> 发起需求，/requirement list 查看已有需求。", nil)
	}
	id, err := sourceUUID(conversation.RequirementID, "requirement ID")
	if err != nil {
		return finish("", err)
	}
	detail, err := h.requirementService().GetRequirement(ctx, service.RequirementActor{WorkspaceID: inst.WorkspaceID, UserID: identity.UserID}, id)
	if err != nil || detail.AgentID == nil || *detail.AgentID != uuidToString(inst.AgentID) {
		return finish("当前需求已不可访问，请用 /requirement list 重新选择。", nil)
	}
	return finish(h.popoRequirementStatus(ctx, inst, detail, conversation.Mode), nil)
}

func (h *Handler) popoRequirementStatus(ctx context.Context, inst engine.ResolvedInstallation, detail service.RequirementDetail, mode string) string {
	var source struct{ Title string }
	_ = json.Unmarshal(detail.SourceSnapshot, &source)
	lines := []string{popoConversationModeText(mode), "需求流程 · " + popo.RequirementStateLabel(detail.State), source.Title, "需求 ID：" + detail.ID, "来源：" + detail.SourceHost + "/" + detail.ExternalID}
	workspace, err := h.Queries.GetWorkspace(ctx, inst.WorkspaceID)
	if err == nil {
		root := strings.TrimRight(h.cfg.AppURL, "/") + "/" + url.PathEscape(workspace.Slug)
		if detail.IssueID != nil {
			lines = append(lines, "执行任务："+root+"/issues/"+url.PathEscape(*detail.IssueID))
		}
		lines = append(lines, "需求 Chat："+root+"/chat?requirement="+url.QueryEscape(detail.ID))
	}
	lines = append(lines, popo.RequirementNextStep(detail.State))
	if detail.State == "awaiting_approval" && !detail.Permissions.CanApprove {
		lines = append(lines, "你当前无批准权限，请等待 approver 确认。")
	}
	if detail.State == "in_review" && !detail.Permissions.CanAccept {
		lines = append(lines, "你当前无验收权限，请等待 requester 或 approver 确认。")
	}
	return strings.Join(lines, "\n")
}
