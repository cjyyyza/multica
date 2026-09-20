package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var _ engine.IssueFollow = (*Handler)(nil)

func (h *Handler) CreateMemberComment(ctx context.Context, in engine.MemberCommentInput) (engine.MemberCommentResult, error) {
	content := sanitizeNullBytes(strings.TrimSpace(in.Content))
	if content == "" || !in.IssueID.Valid || !in.ActorUserID.Valid {
		return engine.MemberCommentResult{}, errors.New("comment content, issue, and actor are required")
	}
	if in.MessageID != "" && in.InstallationID.Valid {
		if existing, err := h.Queries.GetChannelInboundWrite(ctx, db.GetChannelInboundWriteParams{
			InstallationID: in.InstallationID,
			MessageID:      in.MessageID,
			Kind:           engine.InboundWriteKindComment,
		}); err == nil && existing.CommentID.Valid {
			comment, cerr := h.Queries.GetComment(ctx, existing.CommentID)
			if cerr == nil {
				return engine.MemberCommentResult{Comment: comment, IdempotentReplay: true}, nil
			}
		}
	}

	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: in.IssueID, WorkspaceID: in.WorkspaceID,
	})
	if err != nil {
		return engine.MemberCommentResult{}, err
	}

	var parentComment *db.Comment
	parentID := in.ParentID
	if parentID.Valid {
		parent, perr := h.Queries.GetComment(ctx, parentID)
		if perr != nil || parent.IssueID != issue.ID {
			parentID = pgtype.UUID{}
		} else {
			parentComment = &parent
		}
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return engine.MemberCommentResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "member",
		AuthorID:    in.ActorUserID,
		Content:     content,
		Type:        "comment",
		ParentID:    parentID,
	})
	if err != nil {
		return engine.MemberCommentResult{}, err
	}
	if in.MessageID != "" && in.InstallationID.Valid {
		channelType := string(in.ChannelType)
		if channelType == "" {
			channelType = "popo"
		}
		if _, err := qtx.InsertChannelInboundWrite(ctx, db.InsertChannelInboundWriteParams{
			WorkspaceID:    issue.WorkspaceID,
			InstallationID: in.InstallationID,
			ChannelType:    channelType,
			MessageID:      in.MessageID,
			Kind:           engine.InboundWriteKindComment,
			IssueID:        issue.ID,
			CommentID:      created.ID,
		}); err != nil {
			if uniqueViolation(err) {
				existing, loadErr := h.Queries.GetChannelInboundWrite(ctx, db.GetChannelInboundWriteParams{
					InstallationID: in.InstallationID,
					MessageID:      in.MessageID,
					Kind:           engine.InboundWriteKindComment,
				})
				if loadErr == nil && existing.CommentID.Valid {
					comment, cerr := h.Queries.GetComment(ctx, existing.CommentID)
					if cerr == nil {
						return engine.MemberCommentResult{Comment: comment, IdempotentReplay: true}, nil
					}
				}
			}
			return engine.MemberCommentResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return engine.MemberCommentResult{}, err
	}
	comment := created.Comment()

	authorID := util.UUIDToString(in.ActorUserID)
	h.publish(protocol.EventCommentCreated, util.UUIDToString(issue.WorkspaceID), "member", authorID, map[string]any{
		"comment":        commentEventMap(comment),
		"issue_title":    issue.Title,
		"issue_status":   issue.Status,
		"issue_revision": created.IssueRevision,
	})
	var rootComment *db.Comment
	if parentID.Valid {
		if root, rerr := h.Queries.GetThreadRoot(ctx, db.GetThreadRootParams{
			CommentID: parentID, WorkspaceID: issue.WorkspaceID,
		}); rerr == nil {
			rootComment = &root
		}
	}
	if h.TaskService != nil {
		h.TaskService.AutoUnresolveThreadOnReply(ctx, rootComment, util.UUIDToString(issue.WorkspaceID), "member", authorID)
	}
	h.triggerTasksForComment(ctx, issue, comment, parentComment, "member", authorID, authorID, nil)
	return engine.MemberCommentResult{Comment: comment}, nil
}

func commentEventMap(c db.Comment) map[string]any {
	return map[string]any{
		"id":             util.UUIDToString(c.ID),
		"issue_id":       util.UUIDToString(c.IssueID),
		"author_type":    c.AuthorType,
		"author_id":      util.UUIDToString(c.AuthorID),
		"content":        c.Content,
		"type":           c.Type,
		"parent_id":      util.UUIDToPtr(c.ParentID),
		"source_task_id": util.UUIDToPtr(c.SourceTaskID),
		"created_at":     util.TimestampToString(c.CreatedAt),
	}
}

func (h *Handler) ResolveIssue(ctx context.Context, workspaceID pgtype.UUID, identifier string) (engine.FollowIssue, error) {
	identifier = strings.TrimSpace(identifier)
	if !workspaceID.Valid || identifier == "" {
		return engine.FollowIssue{}, pgx.ErrNoRows
	}
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return engine.FollowIssue{}, err
	}
	var issue db.Issue
	if parsed, perr := util.ParseUUID(identifier); perr == nil && parsed.Valid {
		issue, err = h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID: parsed, WorkspaceID: workspaceID,
		})
	} else {
		parts := splitIdentifier(identifier)
		if parts == nil {
			return engine.FollowIssue{}, pgx.ErrNoRows
		}
		prefix := issuePrefixForWorkspace(ws)
		if prefix == "" || !strings.EqualFold(parts.prefix, prefix) {
			return engine.FollowIssue{}, pgx.ErrNoRows
		}
		issue, err = h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
			WorkspaceID: workspaceID, Number: parts.number,
		})
	}
	if err != nil {
		return engine.FollowIssue{}, err
	}
	return engine.FollowIssue{
		Issue:      issue,
		Identifier: service.IssueIdentifier(issuePrefixForWorkspace(ws), issue.Number),
		Prefix:     issuePrefixForWorkspace(ws),
		Slug:       ws.Slug,
	}, nil
}

func (h *Handler) ListActiveRuns(ctx context.Context, issueID pgtype.UUID) ([]engine.FollowRun, error) {
	tasks, err := h.Queries.ListActiveTasksByIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.FollowRun, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, engine.FollowRun{Task: task, Status: task.Status})
	}
	return out, nil
}

func (h *Handler) CancelRun(ctx context.Context, in engine.CancelRunInput) (engine.FollowRun, error) {
	if h.TaskService == nil || !in.TaskID.Valid || !in.ActorUserID.Valid {
		return engine.FollowRun{}, errors.New("cancel requires a task and actor")
	}
	if in.MessageID != "" && in.InstallationID.Valid {
		if existing, err := h.Queries.GetChannelInboundWrite(ctx, db.GetChannelInboundWriteParams{
			InstallationID: in.InstallationID,
			MessageID:      in.MessageID,
			Kind:           engine.InboundWriteKindCancel,
		}); err == nil && existing.TaskID.Valid {
			task, terr := h.Queries.GetAgentTask(ctx, existing.TaskID)
			if terr == nil {
				return engine.FollowRun{Task: task, Status: task.Status}, nil
			}
		}
	}
	actor := h.taskCancellationActor(ctx, "member", util.UUIDToString(in.ActorUserID))
	task, err := h.TaskService.CancelTaskByUser(ctx, in.TaskID, actor)
	if err != nil {
		return engine.FollowRun{}, err
	}
	if in.MessageID != "" && in.InstallationID.Valid {
		channelType := string(in.ChannelType)
		if channelType == "" {
			channelType = "popo"
		}
		_, _ = h.Queries.InsertChannelInboundWrite(ctx, db.InsertChannelInboundWriteParams{
			WorkspaceID:    in.WorkspaceID,
			InstallationID: in.InstallationID,
			ChannelType:    channelType,
			MessageID:      in.MessageID,
			Kind:           engine.InboundWriteKindCancel,
			IssueID:        task.IssueID,
			TaskID:         task.ID,
		})
	}
	return engine.FollowRun{Task: *task, Status: task.Status}, nil
}

func (h *Handler) LookupQuote(ctx context.Context, in engine.QuoteLookupInput) (engine.QuotedTarget, error) {
	if !in.InstallationID.Valid || strings.TrimSpace(in.MessageID) == "" {
		return engine.QuotedTarget{}, engine.ErrQuoteNotFound
	}
	row, err := h.Queries.GetChannelOutboundMessageForQuote(ctx, db.GetChannelOutboundMessageForQuoteParams{
		InstallationID:   in.InstallationID,
		ChannelMessageID: in.MessageID,
	})
	if err != nil {
		return engine.QuotedTarget{}, engine.ErrQuoteNotFound
	}
	if in.ChatID != "" && row.ChannelChatID != in.ChatID {
		return engine.QuotedTarget{}, engine.ErrQuoteNotFound
	}
	return engine.QuotedTarget{
		IssueID:      row.IssueID,
		CommentID:    row.CommentID,
		TaskID:       row.TaskID,
		OutboundKind: row.OutboundKind,
		ChatID:       row.ChannelChatID,
	}, nil
}

func (h *Handler) LoadInboundWrite(ctx context.Context, installationID pgtype.UUID, messageID, kind string) (engine.InboundWrite, error) {
	row, err := h.Queries.GetChannelInboundWrite(ctx, db.GetChannelInboundWriteParams{
		InstallationID: installationID,
		MessageID:      messageID,
		Kind:           kind,
	})
	if err != nil {
		return engine.InboundWrite{}, err
	}
	return engine.InboundWrite{
		Kind:      row.Kind,
		IssueID:   row.IssueID,
		CommentID: row.CommentID,
		TaskID:    row.TaskID,
	}, nil
}

func (h *Handler) OwnerLabel(ctx context.Context, issue db.Issue) string {
	if !issue.AssigneeID.Valid || !issue.AssigneeType.Valid {
		return ""
	}
	switch issue.AssigneeType.String {
	case "agent":
		agent, err := h.Queries.GetAgent(ctx, issue.AssigneeID)
		if err == nil {
			return agent.Name
		}
	case "member":
		user, err := h.Queries.GetUser(ctx, issue.AssigneeID)
		if err == nil {
			return user.Name
		}
	}
	return "another assignee"
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
