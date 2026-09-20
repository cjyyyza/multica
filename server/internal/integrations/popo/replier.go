package popo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	msgFreshPending     = "✅ Fresh start ready. Your next chat message will run without previous context."
	msgChatStarted      = "✅ Started a new Multica chat. Your next message will enter it."
	msgIssueUsage       = "Please include an issue title. Use:\n\n/issue <title>\n[description] (optional)"
	msgIssueNotMember   = "You're not a member of this Multica workspace, so I can't file an issue for you. Ask a workspace admin to invite you, then send the command again."
	msgIssueDisabled    = "This POPO bot isn't connected to Multica (or was disconnected). Ask a workspace admin to reconnect it."
	msgAgentOffline     = "This agent is offline right now. Try again when a runtime is connected."
	msgAgentArchived    = "This agent has been archived, so it can't reply."
	msgBindingGroupHint = "Please message this bot in a private chat first so I can send you a Multica account link."
)

type bindingMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, popoUserID string) (BindingToken, error)
}

type OutboundReplier struct {
	binding     bindingMinter
	queue       Enqueuer
	appURL      string
	bindingPath string
	logger      *slog.Logger
}

type OutboundReplierConfig struct {
	Binding     bindingMinter
	Queue       Enqueuer
	AppURL      string
	BindingPath string
	Logger      *slog.Logger
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/popo/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	return &OutboundReplier{
		binding:     cfg.Binding,
		queue:       cfg.Queue,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
		logger:      logger,
	}
}

func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "popo replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		_ = r.post(ctx, inst, msg, msgAgentOffline)
	case engine.OutcomeAgentArchived:
		_ = r.post(ctx, inst, msg, msgAgentArchived)
	case engine.OutcomeFreshPending:
		_ = r.post(ctx, inst, msg, msgFreshPending)
	case engine.OutcomeChatStarted:
		_ = r.post(ctx, inst, msg, msgChatStarted)
	case engine.OutcomeIssueUsage:
		_ = r.post(ctx, inst, msg, msgIssueUsage)
	case engine.OutcomeIngested:
		if res.IssueID.Valid {
			text := issueCreatedText(res, r.appURL)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
			_ = r.postIssue(ctx, inst, msg, res, text, "issue_created")
		}
	case engine.OutcomeIssueFollow:
		if strings.TrimSpace(res.ReplyText) != "" {
			kind := "issue_ack"
			if res.CommentID.Valid {
				kind = "issue_comment_ack"
			}
			text := res.ReplyText
			if link := channel.IssueWebLink(r.appURL, res.IssueWorkspaceSlug, res.IssueIdentifier); link != "" && !strings.Contains(text, link) {
				text += "\n" + link
			}
			_ = r.postIssue(ctx, inst, msg, res, text, kind)
		}
	case engine.OutcomeDropped:
		if text := droppedReplyText(res, msg); text != "" {
			_ = r.post(ctx, inst, msg, text)
		}
	}
}

func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) error {
	if msg.Source.ChatType == channel.ChatTypeGroup {
		return r.post(ctx, inst, msg, msgBindingGroupHint)
	}
	sender := res.Sender
	if sender == "" {
		sender = msg.Source.SenderID
	}
	if sender == "" {
		return errors.New("missing sender id")
	}
	if r.binding == nil {
		return errors.New("binding service not configured")
	}
	if r.appURL == "" {
		return errors.New("app url not configured")
	}
	token, err := r.binding.Mint(ctx, inst.WorkspaceID, inst.ID, sender)
	if err != nil {
		return fmt.Errorf("mint binding token: %w", err)
	}
	bindURL := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	text := "👋 To start chatting with me, link your POPO account to Multica:\n" + bindURL + "\n(This link expires in 15 minutes.)"
	return r.post(ctx, inst, msg, text)
}

func (r *OutboundReplier) post(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	if r.queue == nil {
		return errors.New("popo outbound queue not configured")
	}
	robotID := ""
	var bridgeID pgtype.UUID
	if row, ok := inst.Platform.(db.ChannelInstallation); ok {
		info := DecodePublicConfig(row.Config)
		robotID = info.RobotID
		if parsed, err := util.ParseUUID(info.BridgeID); err == nil {
			bridgeID = parsed
		}
	}
	chatType := string(msg.Source.ChatType)
	if chatType == "" {
		chatType = string(channel.ChatTypeP2P)
	}
	return r.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		BridgeID:       bridgeID,
		ChatID:         msg.Source.ChatID,
		ChatType:       chatType,
		RobotID:        robotID,
		Content:        text,
	})
}

func (r *OutboundReplier) postIssue(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result, text, kind string) error {
	if r.queue == nil {
		return errors.New("popo outbound queue not configured")
	}
	robotID := ""
	var bridgeID pgtype.UUID
	if row, ok := inst.Platform.(db.ChannelInstallation); ok {
		info := DecodePublicConfig(row.Config)
		robotID = info.RobotID
		if parsed, err := util.ParseUUID(info.BridgeID); err == nil {
			bridgeID = parsed
		}
	}
	chatType := string(msg.Source.ChatType)
	if chatType == "" {
		chatType = string(channel.ChatTypeP2P)
	}
	sourceKey, issueStatus := res.ReplyKey, ""
	if kind == "issue_created" && !res.IssueDuplicate {
		sourceKey, issueStatus = "issue_created:"+uuidString(res.IssueID), "todo"
	}
	return r.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		BridgeID:       bridgeID,
		ChatID:         msg.Source.ChatID,
		ChatType:       chatType,
		RobotID:        robotID,
		Content:        text,
		IssueID:        res.IssueID,
		CommentID:      res.CommentID,
		BindingID:      res.ChannelBindingID,
		RouteRevision:  res.ChannelRouteRevision,
		OutboundKind:   kind,
		SourceKey:      sourceKey,
		IssueStatus:    issueStatus,
	})
}

func issueCreatedText(res engine.Result, appURL string) string {
	id := issueResultIdentifier(res)
	title := strings.TrimSpace(res.IssueTitle)
	text := "✅ Created " + id
	if title != "" {
		text += " — " + title
	}
	if link := channel.IssueWebLink(appURL, res.IssueWorkspaceSlug, id); link != "" {
		text += "\n" + link
	}
	return text
}

func issueDuplicateText(res engine.Result) string {
	id := issueResultIdentifier(res)
	title := strings.TrimSpace(res.IssueTitle)
	if title == "" {
		return "⚠️ Not created — active issue " + id + " already exists."
	}
	return "⚠️ Not created — active issue " + id + " already exists: " + title
}

func issueResultIdentifier(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	if res.IssueNumber > 0 {
		return fmt.Sprintf("#%d", res.IssueNumber)
	}
	return util.UUIDToString(res.IssueID)
}

func isAddressedIssueCommand(msg channel.InboundMessage) bool {
	if !msg.AddressedToBot {
		return false
	}
	source := msg.CommandText
	if source == "" {
		source = msg.Text
	}
	_, ok := engine.ParseIssueCommand(source)
	return ok
}

func droppedReplyText(res engine.Result, msg channel.InboundMessage) string {
	if !isAddressedIssueCommand(msg) {
		return ""
	}
	switch res.DropReason {
	case engine.DropReasonNonWorkspaceMember:
		return msgIssueNotMember
	case engine.DropReasonRevokedInstallation:
		return msgIssueDisabled
	default:
		return ""
	}
}
