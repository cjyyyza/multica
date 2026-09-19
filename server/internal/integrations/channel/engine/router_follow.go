package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	msgFollowReplyUsage    = "Please include an issue key and comment. Use:\n\n/reply ISSUE-123 <comment>"
	msgFollowStatusUsage   = "Please include an issue key. Use:\n\n/status ISSUE-123"
	msgFollowStopUsage     = "Please include an issue key, or quote a specific run message. Use:\n\n/stop ISSUE-123"
	msgFollowStopNone      = "There is no cancellable run on that issue."
	msgFollowStopAmbiguous = "I couldn't tell which run to cancel. Quote a specific run message, or use /stop ISSUE-123."
	msgFollowQuoteUnknown  = "I couldn't match that quoted message to an issue in this chat."
	msgFollowIssueMissing  = "I couldn't find that issue in this workspace."
	msgFollowCommentAck    = "✅ Commented on %s"
	msgFollowStopAck       = "✅ Cancelled the current run on %s. Issue status is unchanged (%s)."
	msgFollowReassigned    = "This issue is now assigned to %s. I won't keep running it."
)

func (r *Router) handleFollow(ctx context.Context, set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, msg channel.InboundMessage) (Result, bool, error) {
	if r.follow == nil {
		return Result{}, false, nil
	}
	if _, isIssue := ParseIssueCommand(msg.CommandText); isIssue {
		return Result{}, false, nil
	}
	cmd, hasCmd := ParseFollowCommand(msg.CommandText)
	quoted, hasQuote, quoteErr := r.lookupQuote(ctx, inst, msg)
	if quoteErr != nil {
		return Result{}, true, quoteErr
	}

	switch {
	case hasCmd && cmd.Kind == FollowCommandReply:
		res, err := r.followReply(ctx, set, inst, identity, msg, cmd, quoted, hasQuote)
		return res, true, err
	case hasCmd && cmd.Kind == FollowCommandStatus:
		res, err := r.followStatus(ctx, inst, msg, cmd)
		return res, true, err
	case hasCmd && cmd.Kind == FollowCommandStop:
		res, err := r.followStop(ctx, inst, identity, msg, cmd, quoted, hasQuote)
		return res, true, err
	case hasQuote && quoted.IssueID.Valid && strings.TrimSpace(followCommentBody(msg)) != "":
		res, err := r.followQuotedComment(ctx, set, inst, identity, msg, quoted)
		return res, true, err
	default:
		return Result{}, false, nil
	}
}

func followCommentBody(msg channel.InboundMessage) string {
	body := strings.TrimSpace(msg.CommandText)
	if body == "" {
		body = strings.TrimSpace(msg.Text)
	}
	return body
}

func (r *Router) lookupQuote(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage) (QuotedTarget, bool, error) {
	if r.follow == nil || msg.ReplyTo == nil {
		return QuotedTarget{}, false, nil
	}
	messageID := strings.TrimSpace(msg.ReplyTo.MessageID)
	if messageID == "" {
		return QuotedTarget{}, false, nil
	}
	target, err := r.follow.LookupQuote(ctx, QuoteLookupInput{
		InstallationID: inst.ID,
		WorkspaceID:    inst.WorkspaceID,
		ChatID:         msg.Source.ChatID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, ErrQuoteNotFound) {
			return QuotedTarget{}, false, nil
		}
		return QuotedTarget{}, false, err
	}
	return target, target.IssueID.Valid || target.TaskID.Valid, nil
}

func (r *Router) followReply(ctx context.Context, set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, msg channel.InboundMessage, cmd FollowCommand, quoted QuotedTarget, hasQuote bool) (Result, error) {
	if cmd.Identifier == "" || cmd.Body == "" {
		return r.followResult(inst, msg, msgFollowReplyUsage), nil
	}
	issue, err := r.follow.ResolveIssue(ctx, inst.WorkspaceID, cmd.Identifier)
	if err != nil {
		return r.followResult(inst, msg, msgFollowIssueMissing), nil
	}
	if blocked, text := r.followOwnership(ctx, inst, issue.Issue); blocked {
		return r.followIssueResult(inst, msg, issue, text), nil
	}
	parentID := pgtype.UUID{}
	if hasQuote && quoted.IssueID == issue.Issue.ID {
		parentID = quoted.CommentID
	}
	return r.createFollowComment(ctx, set, inst, identity, msg, issue, cmd.Body, parentID)
}

func (r *Router) followQuotedComment(ctx context.Context, set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, msg channel.InboundMessage, quoted QuotedTarget) (Result, error) {
	issue, err := r.resolveQuotedIssue(ctx, inst, quoted.IssueID)
	if err != nil {
		return r.followResult(inst, msg, msgFollowQuoteUnknown), nil
	}
	if blocked, text := r.followOwnership(ctx, inst, issue.Issue); blocked {
		return r.followIssueResult(inst, msg, issue, text), nil
	}
	return r.createFollowComment(ctx, set, inst, identity, msg, issue, followCommentBody(msg), quoted.CommentID)
}

func (r *Router) resolveQuotedIssue(ctx context.Context, inst ResolvedInstallation, issueID pgtype.UUID) (FollowIssue, error) {
	// ResolveIssue expects PREFIX-N. Quoted targets carry a UUID; Handler
	// also accepts UUIDs through the same method.
	return r.follow.ResolveIssue(ctx, inst.WorkspaceID, util.UUIDToString(issueID))
}

func (r *Router) createFollowComment(ctx context.Context, set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, msg channel.InboundMessage, issue FollowIssue, content string, parentID pgtype.UUID) (Result, error) {
	created, err := r.follow.CreateMemberComment(ctx, MemberCommentInput{
		WorkspaceID:    inst.WorkspaceID,
		IssueID:        issue.Issue.ID,
		ActorUserID:    identity.UserID,
		Content:        content,
		ParentID:       parentID,
		InstallationID: inst.ID,
		ChannelType:    msg.Source.ChannelType,
		MessageID:      msg.MessageID,
	})
	if err != nil {
		return Result{}, err
	}
	res := r.followIssueResult(inst, msg, issue, fmt.Sprintf(msgFollowCommentAck, issue.Identifier))
	res.CommentID = created.Comment.ID
	if created.IdempotentReplay {
		res.Outcome = OutcomeDropped
		res.DropReason = DropReasonDuplicate
		res.ReplyText = ""
	}
	return res, nil
}

func (r *Router) followStatus(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage, cmd FollowCommand) (Result, error) {
	if cmd.Identifier == "" {
		return r.followResult(inst, msg, msgFollowStatusUsage), nil
	}
	issue, err := r.follow.ResolveIssue(ctx, inst.WorkspaceID, cmd.Identifier)
	if err != nil {
		return r.followResult(inst, msg, msgFollowIssueMissing), nil
	}
	runs, err := r.follow.ListActiveRuns(ctx, issue.Issue.ID)
	if err != nil {
		return Result{}, err
	}
	runStatus := "none"
	if len(runs) > 0 {
		runStatus = runs[0].Status
	}
	link := channel.IssueWebLink("", issue.Slug, issue.Identifier)
	text := fmt.Sprintf("Issue %s — %s\nIssue status: %s\nCurrent run: %s",
		issue.Identifier, strings.TrimSpace(issue.Issue.Title), issue.Issue.Status, runStatus)
	if link != "" {
		text += "\n" + link
	}
	res := r.followIssueResult(inst, msg, issue, text)
	res.IssueStatus = issue.Issue.Status
	res.RunStatus = runStatus
	return res, nil
}

func (r *Router) followStop(ctx context.Context, inst ResolvedInstallation, identity ResolvedIdentity, msg channel.InboundMessage, cmd FollowCommand, quoted QuotedTarget, hasQuote bool) (Result, error) {
	if cmd.Identifier == "" && !(hasQuote && quoted.TaskID.Valid) {
		if hasQuote && !quoted.TaskID.Valid {
			return r.followResult(inst, msg, msgFollowStopAmbiguous), nil
		}
		if cmd.Identifier == "" {
			return r.followResult(inst, msg, msgFollowStopUsage), nil
		}
	}

	var issue FollowIssue
	var taskID pgtype.UUID
	if hasQuote && quoted.TaskID.Valid && (cmd.Identifier == "" || quoted.IssueID.Valid) {
		taskID = quoted.TaskID
		if quoted.IssueID.Valid {
			resolved, err := r.resolveQuotedIssue(ctx, inst, quoted.IssueID)
			if err != nil {
				return r.followResult(inst, msg, msgFollowStopAmbiguous), nil
			}
			issue = resolved
		}
	}
	if !taskID.Valid {
		if cmd.Identifier == "" {
			return r.followResult(inst, msg, msgFollowStopUsage), nil
		}
		resolved, err := r.follow.ResolveIssue(ctx, inst.WorkspaceID, cmd.Identifier)
		if err != nil {
			return r.followResult(inst, msg, msgFollowIssueMissing), nil
		}
		issue = resolved
		if blocked, text := r.followOwnership(ctx, inst, issue.Issue); blocked {
			return r.followIssueResult(inst, msg, issue, text), nil
		}
		runs, err := r.follow.ListActiveRuns(ctx, issue.Issue.ID)
		if err != nil {
			return Result{}, err
		}
		if len(runs) == 0 {
			return r.followIssueResult(inst, msg, issue, msgFollowStopNone), nil
		}
		if len(runs) > 1 && !hasQuote {
			return r.followIssueResult(inst, msg, issue, msgFollowStopAmbiguous), nil
		}
		taskID = runs[0].Task.ID
	} else if issue.Issue.ID.Valid {
		if blocked, text := r.followOwnership(ctx, inst, issue.Issue); blocked {
			return r.followIssueResult(inst, msg, issue, text), nil
		}
	}

	cancelled, err := r.follow.CancelRun(ctx, CancelRunInput{
		WorkspaceID:    inst.WorkspaceID,
		ActorUserID:    identity.UserID,
		TaskID:         taskID,
		InstallationID: inst.ID,
		ChannelType:    msg.Source.ChannelType,
		MessageID:      msg.MessageID,
	})
	if err != nil {
		return Result{}, err
	}
	if !issue.Issue.ID.Valid && cancelled.Task.IssueID.Valid {
		resolved, resolveErr := r.resolveQuotedIssue(ctx, inst, cancelled.Task.IssueID)
		if resolveErr == nil {
			issue = resolved
		}
	}
	status := issue.Issue.Status
	if status == "" {
		status = "unchanged"
	}
	ident := issue.Identifier
	if ident == "" {
		ident = util.UUIDToString(cancelled.Task.IssueID)
	}
	res := r.followIssueResult(inst, msg, issue, fmt.Sprintf(msgFollowStopAck, ident, status))
	res.RunStatus = cancelled.Status
	return res, nil
}

func (r *Router) followOwnership(ctx context.Context, inst ResolvedInstallation, issue db.Issue) (blocked bool, text string) {
	if !issue.AssigneeID.Valid || !issue.AssigneeType.Valid {
		return false, ""
	}
	if issue.AssigneeType.String == "agent" && issue.AssigneeID == inst.AgentID {
		return false, ""
	}
	label := r.follow.OwnerLabel(ctx, issue)
	if label == "" {
		label = "another assignee"
	}
	return true, fmt.Sprintf(msgFollowReassigned, label)
}

func (r *Router) followResult(inst ResolvedInstallation, msg channel.InboundMessage, text string) Result {
	return Result{
		Outcome:        OutcomeIssueFollow,
		InstallationID: inst.ID,
		Sender:         msg.Source.SenderID,
		ReplyText:      text,
	}
}

func (r *Router) followIssueResult(inst ResolvedInstallation, msg channel.InboundMessage, issue FollowIssue, text string) Result {
	res := r.followResult(inst, msg, text)
	res.IssueID = issue.Issue.ID
	res.IssueNumber = issue.Issue.Number
	res.IssueTitle = issue.Issue.Title
	res.IssueIdentifier = issue.Identifier
	res.IssueWorkspaceSlug = issue.Slug
	res.IssueStatus = issue.Issue.Status
	return res
}

func (r *Router) recoverDuplicateCommand(ctx context.Context, set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage) (Result, bool) {
	if r.follow == nil || strings.TrimSpace(msg.MessageID) == "" {
		return Result{}, false
	}
	for _, kind := range []string{InboundWriteKindIssue, InboundWriteKindComment, InboundWriteKindCancel} {
		if _, err := r.follow.LoadInboundWrite(ctx, inst.ID, msg.MessageID, kind); err == nil {
			return r.drop(ctx, set, msg, inst.ID, DropReasonDuplicate), true
		}
	}

	identity, err := set.Identity.ResolveSender(ctx, inst, msg)
	if err != nil {
		return Result{}, false
	}

	if parsed, ok := ParseIssueCommand(msg.CommandText); ok && parsed.Title != "" {
		sessionID, sessErr := set.Session.EnsureSession(ctx, EnsureSessionParams{
			Installation: inst, Sender: identity.UserID, Message: msg,
		})
		if sessErr != nil {
			return Result{}, false
		}
		prefix, _ := r.issueWorkspaceIdentity(ctx, inst.WorkspaceID)
		issueRes, createErr := r.createIssue(ctx, inst, set.OriginType, identity.UserID, sessionID, *parsed, prefix, time.Time{}, msg, pgtype.UUID{}, 0)
		if createErr != nil && !errors.Is(createErr, service.ErrActiveDuplicate) {
			return Result{}, false
		}
		issue := issueRes.Issue
		if issueRes.DuplicateIssue != nil {
			issue = *issueRes.DuplicateIssue
		}
		res := Result{
			Outcome:         OutcomeIngested,
			InstallationID:  inst.ID,
			ChatSessionID:   sessionID,
			Sender:          msg.Source.SenderID,
			IssueID:         issue.ID,
			IssueNumber:     issue.Number,
			IssueTitle:      issue.Title,
			IssueIdentifier: service.IssueIdentifier(prefix, issue.Number),
			IssueDuplicate:  issueRes.DuplicateIssue != nil && !issueRes.IdempotentReplay,
		}
		return res, true
	}

	if followRes, handled, followErr := r.handleFollow(ctx, set, inst, identity, msg); handled && followErr == nil {
		return followRes, true
	}
	return Result{}, false
}

// ErrQuoteNotFound means the quoted platform message did not resolve under
// installation + chat + message-id validation.
var ErrQuoteNotFound = errors.New("channel quote not found")
