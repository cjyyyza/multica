package popo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Outbound enqueues the agent's final chat reply as a bridge send command.
type Outbound struct {
	q       outboundQueries
	queue   Enqueuer
	storage mediaStorage
	appURL  string
	logger  *slog.Logger
}

type outboundQueries interface {
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	GetChannelIssueSourceByIssue(ctx context.Context, issueID pgtype.UUID) (db.ChannelIssueSource, error)
	GetChannelInboundWriteByComment(ctx context.Context, commentID pgtype.UUID) (db.ChannelInboundWrite, error)
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
	ListAttachmentsByChatMessage(ctx context.Context, arg db.ListAttachmentsByChatMessageParams) ([]db.Attachment, error)
	ListAttachmentsByComment(ctx context.Context, arg db.ListAttachmentsByCommentParams) ([]db.Attachment, error)
	CreateAttachment(ctx context.Context, arg db.CreateAttachmentParams) (db.CreateAttachmentRow, error)
	LinkAttachmentsToChatMessage(ctx context.Context, arg db.LinkAttachmentsToChatMessageParams) ([]pgtype.UUID, error)
	GetChatMessageByTaskAssistant(ctx context.Context, taskID pgtype.UUID) (db.ChatMessage, error)
}

func NewOutbound(q *db.Queries, queue Enqueuer, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbound{q: q, queue: queue, logger: logger}
}

func (o *Outbound) WithDelivery(store mediaStorage, appURL string) *Outbound {
	if o == nil {
		return o
	}
	o.storage = store
	o.appURL = strings.TrimRight(appURL, "/")
	return o
}

func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleChatDone)
	bus.Subscribe(protocol.EventCommentCreated, o.handleCommentCreated)
	bus.Subscribe(protocol.EventTaskFailed, o.handleTaskTerminal)
	bus.Subscribe(protocol.EventTaskCancelled, o.handleTaskTerminal)
	bus.Subscribe(protocol.EventIssueUpdated, o.handleIssueUpdated)
}

func (o *Outbound) handleChatDone(e events.Event) {
	content := chatDoneContent(e.Payload)
	ctx := context.Background()
	taskID, ok := eventTaskID(e)
	if !ok {
		return
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.logger.WarnContext(ctx, "popo outbound: task delivery lookup failed", "error", err)
		}
		return
	}
	if delivery.ChannelType != string(TypePopo) {
		return
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          delivery.InstallationID,
		ChannelType: string(TypePopo),
	})
	if err != nil || inst.Status != "active" {
		return
	}
	chatID := delivery.ChannelChatID
	if len(delivery.Config) > 0 {
		var cfg popoBindingConfig
		if err := json.Unmarshal(delivery.Config, &cfg); err == nil && cfg.ChatID != "" {
			chatID = cfg.ChatID
		}
	}
	info := DecodePublicConfig(inst.Config)
	var bridgeID pgtype.UUID
	if parsed, err := util.ParseUUID(info.BridgeID); err == nil {
		bridgeID = parsed
	}
	task, _ := o.q.GetAgentTask(ctx, taskID)
	chatType := strings.TrimSpace(delivery.ChatType)
	if chatType == "" {
		chatType = string(channel.ChatTypeP2P)
	}
	messageID := chatDoneMessageID(e.Payload)
	if !messageID.Valid {
		if row, err := o.q.GetChatMessageByTaskAssistant(ctx, taskID); err == nil {
			messageID = row.ID
		}
	}
	var existing []db.Attachment
	if messageID.Valid {
		existing, _ = o.q.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{
			ChatMessageID: messageID,
			WorkspaceID:   inst.WorkspaceID,
		})
	}
	if strings.TrimSpace(content) == "" && len(existing) == 0 {
		return
	}
	link := o.chatWebLink(ctx, inst.WorkspaceID, task.ChatSessionID)
	text, atts := o.prepareOutbound(ctx, inst, content, link, existing, messageID, task.ChatSessionID, task.ID, task.AgentID, "agent")
	if err := o.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		BridgeID:       bridgeID,
		ChatID:         chatID,
		ChatType:       chatType,
		RobotID:        info.RobotID,
		Content:        text,
		IssueID:        task.IssueID,
		TaskID:         taskID,
		BindingID:      delivery.BindingID,
		RouteRevision:  delivery.RouteRevision,
		OutboundKind:   "task_reply",
		Attachments:    atts,
	}); err != nil {
		o.logger.WarnContext(ctx, "popo outbound: enqueue failed",
			"installation_id", util.UUIDToString(inst.ID), "error", err)
	}
}

func eventTaskID(e events.Event) (pgtype.UUID, bool) {
	var raw string
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		raw = p.TaskID
	case map[string]any:
		raw, _ = p["task_id"].(string)
	}
	id, err := util.ParseUUID(raw)
	return id, err == nil && id.Valid
}

func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

func chatDoneMessageID(payload any) pgtype.UUID {
	var raw string
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		raw = p.MessageID
	case map[string]any:
		raw, _ = p["message_id"].(string)
	}
	id, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func clipOutboundText(text, link string) (clipped string, overflow bool) {
	text = strings.TrimSpace(text)
	overflow = utf8.RuneCountInString(text) > MaxOutboundTextRunes
	if overflow {
		runes := []rune(text)
		clipped = string(runes[:MaxOutboundTextRunes]) + "…"
	} else {
		clipped = text
	}
	if link != "" && !strings.Contains(clipped, link) {
		if clipped == "" {
			clipped = link
		} else {
			clipped += "\n" + link
		}
	}
	return clipped, overflow
}

func sendAttachmentsFromRows(rows []db.Attachment) []SendAttachment {
	out := make([]SendAttachment, 0, len(rows))
	for _, row := range rows {
		out = append(out, sendAttachmentFrom(row.ID, row.Filename, row.ContentType))
	}
	return out
}

func (o *Outbound) workspaceSlug(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := o.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return ws.Slug
}

func (o *Outbound) chatWebLink(ctx context.Context, workspaceID, sessionID pgtype.UUID) string {
	if o.appURL == "" || !sessionID.Valid {
		return ""
	}
	slug := o.workspaceSlug(ctx, workspaceID)
	if slug == "" {
		return ""
	}
	return o.appURL + "/" + url.PathEscape(slug) + "/chat?session=" + url.QueryEscape(util.UUIDToString(sessionID))
}

func (o *Outbound) issueWebLink(ctx context.Context, issueID pgtype.UUID) string {
	if o.appURL == "" || !issueID.Valid {
		return ""
	}
	issue, err := o.q.GetIssue(ctx, issueID)
	if err != nil {
		return ""
	}
	ident := o.issueIdentifier(ctx, issueID)
	return channel.IssueWebLink(o.appURL, o.workspaceSlug(ctx, issue.WorkspaceID), ident)
}

func (o *Outbound) prepareOutbound(
	ctx context.Context,
	inst db.ChannelInstallation,
	text, link string,
	existing []db.Attachment,
	chatMessageID, chatSessionID, taskID, uploaderID pgtype.UUID,
	uploaderType string,
) (string, []SendAttachment) {
	clipped, overflow := clipOutboundText(text, link)
	atts := sendAttachmentsFromRows(existing)
	if overflow && o.storage != nil {
		if extra, err := o.persistOverflowText(ctx, inst.WorkspaceID, chatSessionID, chatMessageID, taskID, uploaderID, uploaderType, text); err != nil {
			o.logger.WarnContext(ctx, "popo outbound: long text file failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		} else {
			atts = append(atts, extra)
		}
	}
	return clipped, atts
}

func (o *Outbound) persistOverflowText(
	ctx context.Context,
	workspaceID, chatSessionID, chatMessageID, taskID, uploaderID pgtype.UUID,
	uploaderType, body string,
) (SendAttachment, error) {
	if !workspaceID.Valid || !uploaderID.Valid {
		return SendAttachment{}, errors.New("missing workspace or uploader")
	}
	id := dbid.NewV7()
	filename := "reply.txt"
	contentType := "text/plain; charset=utf-8"
	key := path.Join("workspaces", util.UUIDToString(workspaceID), "popo", "outbound", util.UUIDToString(id))
	data := []byte(body)
	objectURL, err := o.storage.Upload(ctx, key, data, contentType, filename)
	if err != nil {
		return SendAttachment{}, err
	}
	row, err := o.q.CreateAttachment(ctx, db.CreateAttachmentParams{
		ID:            id,
		WorkspaceID:   workspaceID,
		ChatSessionID: chatSessionID,
		TaskID:        taskID,
		UploaderType:  uploaderType,
		UploaderID:    uploaderID,
		Filename:      filename,
		Url:           objectURL,
		ContentType:   "text/plain",
		SizeBytes:     int64(len(data)),
	})
	if err != nil {
		return SendAttachment{}, err
	}
	if chatMessageID.Valid && chatSessionID.Valid {
		_, _ = o.q.LinkAttachmentsToChatMessage(ctx, db.LinkAttachmentsToChatMessageParams{
			ChatMessageID: chatMessageID,
			ChatSessionID: chatSessionID,
			WorkspaceID:   workspaceID,
			UploaderType:  uploaderType,
			UploaderID:    uploaderID,
			AttachmentIds: []pgtype.UUID{row.ID},
		})
	}
	return sendAttachmentFrom(row.ID, row.Filename, row.ContentType), nil
}
