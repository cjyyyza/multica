package popo

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (o *Outbound) handleCommentCreated(e events.Event) {
	o.enqueueComment(context.Background(), e)
}

func (o *Outbound) enqueueComment(ctx context.Context, e events.Event) {
	var envelope struct {
		Comment struct {
			ID         string `json:"id"`
			IssueID    string `json:"issue_id"`
			Content    string `json:"content"`
			AuthorType string `json:"author_type"`
		} `json:"comment"`
	}
	if !decodeEventPayload(e.Payload, &envelope) {
		return
	}
	issueID, ok := parsePayloadUUID(envelope.Comment.IssueID)
	if !ok {
		return
	}
	content := strings.TrimSpace(envelope.Comment.Content)
	commentID, _ := parsePayloadUUID(envelope.Comment.ID)
	authorType := envelope.Comment.AuthorType
	if commentID.Valid && strings.EqualFold(authorType, "member") {
		if write, err := o.q.GetChannelInboundWriteByComment(ctx, commentID); err == nil && write.ChannelType == string(TypePopo) {
			return
		}
	}
	source, err := o.q.GetChannelIssueSourceByIssue(ctx, issueID)
	if err != nil {
		return
	}
	if source.ChannelType != string(TypePopo) {
		return
	}
	if content == "" {
		attachments, err := o.q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
			CommentID: commentID, WorkspaceID: source.WorkspaceID,
		})
		if err != nil || len(attachments) == 0 {
			return
		}
	}
	author := strings.TrimSpace(authorType)
	if author == "" {
		author = "member"
	}
	text := fmt.Sprintf("New comment on this issue (%s):\n%s", author, content)
	o.enqueueSource(ctx, source, issueID, commentID, pgtype.UUID{}, text, "issue_comment")
}

func (o *Outbound) handleTaskTerminal(e events.Event) {
	o.enqueueTaskTerminal(context.Background(), e)
}

func (o *Outbound) enqueueTaskTerminal(ctx context.Context, e events.Event) {
	o.enqueueTaskReply(ctx, e)
}

func (o *Outbound) handleIssueUpdated(e events.Event) {
	var envelope struct {
		StatusChanged bool `json:"status_changed"`
		Issue         struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"issue"`
	}
	if !decodeEventPayload(e.Payload, &envelope) || !envelope.StatusChanged {
		return
	}
	issueID, ok := parsePayloadUUID(envelope.Issue.ID)
	if !ok {
		return
	}
	status := strings.TrimSpace(envelope.Issue.Status)
	if status == "" {
		return
	}
	ctx := context.Background()
	source, err := o.q.GetChannelIssueSourceByIssue(ctx, issueID)
	if err != nil || source.ChannelType != string(TypePopo) {
		return
	}
	ident := o.issueIdentifier(ctx, issueID)
	text := fmt.Sprintf("%s status is now %s. Run status is tracked separately.", ident, status)
	o.enqueueSource(ctx, source, issueID, pgtype.UUID{}, pgtype.UUID{}, text, "issue_status")
}

func (o *Outbound) issueIdentifier(ctx context.Context, issueID pgtype.UUID) string {
	issue, err := o.q.GetIssue(ctx, issueID)
	if err != nil {
		return util.UUIDToString(issueID)
	}
	prefix := ""
	if ws, werr := o.q.GetWorkspace(ctx, issue.WorkspaceID); werr == nil {
		prefix = ws.IssuePrefix
	}
	return service.IssueIdentifier(prefix, issue.Number)
}

func (o *Outbound) enqueueSource(ctx context.Context, source db.ChannelIssueSource, issueID, commentID, taskID pgtype.UUID, text, kind string) {
	if o.queue == nil || strings.TrimSpace(text) == "" {
		return
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          source.InstallationID,
		ChannelType: string(TypePopo),
	})
	if err != nil || inst.Status != "active" {
		return
	}
	sourceKey := kind + ":" + uuidString(issueID)
	issueStatus := ""
	if commentID.Valid {
		sourceKey = kind + ":" + uuidString(commentID)
	}
	if taskID.Valid {
		sourceKey = kind + ":" + uuidString(taskID)
	}
	if kind == "issue_created" {
		issueStatus = "todo"
	}
	if kind == "issue_status" {
		issue, err := o.q.GetIssue(ctx, issueID)
		if err != nil {
			return
		}
		issueStatus = issue.Status
		sourceKey += ":" + strconv.FormatInt(issue.Revision, 10)
		if o.durable != nil {
			previous, err := o.durable.GetLatestPopoIssueStatus(ctx, db.GetLatestPopoIssueStatusParams{InstallationID: inst.ID, Column2: uuidString(issueID)})
			if err == nil && previous == issueStatus {
				return
			}
		}
		text = fmt.Sprintf("%s status is now %s. Run status is tracked separately.", o.issueIdentifier(ctx, issueID), issueStatus)
	}
	if o.sourceQueued(ctx, inst.ID, sourceKey) {
		return
	}
	info := DecodePublicConfig(inst.Config)
	var bridgeID pgtype.UUID
	if parsed, err := util.ParseUUID(info.BridgeID); err == nil {
		bridgeID = parsed
	}
	chatType := source.ChatType
	if chatType == "" {
		chatType = string(channel.ChatTypeP2P)
	}
	var existing []db.Attachment
	if commentID.Valid {
		existing, _ = o.q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
			CommentID:   commentID,
			WorkspaceID: inst.WorkspaceID,
		})
	}
	link := o.issueWebLink(ctx, issueID)
	text, atts := o.prepareOutbound(ctx, inst, text, link, existing, pgtype.UUID{}, pgtype.UUID{}, taskID, inst.AgentID, "agent")
	if err := o.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		BridgeID:       bridgeID,
		ChatID:         source.ChannelChatID,
		ChatType:       chatType,
		RobotID:        info.RobotID,
		Content:        text,
		IssueID:        issueID,
		CommentID:      commentID,
		TaskID:         taskID,
		BindingID:      source.BindingID,
		RouteRevision:  source.RouteRevision,
		OutboundKind:   kind,
		SourceKey:      sourceKey,
		IssueStatus:    issueStatus,
		Attachments:    atts,
	}); err != nil {
		o.logger.WarnContext(ctx, "popo issue follow: enqueue failed",
			"installation_id", util.UUIDToString(inst.ID), "error", err)
	}
}

func decodeEventPayload(payload any, dest any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, dest) == nil
}

func parsePayloadUUID(v any) (pgtype.UUID, bool) {
	s, _ := v.(string)
	id, err := util.ParseUUID(s)
	return id, err == nil && id.Valid
}
