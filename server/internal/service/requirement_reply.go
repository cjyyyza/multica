package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ReplyRequirementRequest struct {
	ExpectedIssueRevision int64  `json:"expectedIssueRevision"`
	QuestionCommentID     string `json:"questionCommentId"`
	Content               string `json:"content"`
}

type RequirementBlockedQuestion struct {
	CommentID    string `json:"commentId"`
	WakeupID     string `json:"wakeupId"`
	TargetUserID string `json:"targetUserId"`
	AgentID      string `json:"agentId"`
	Content      string `json:"content"`
	CanReply     bool   `json:"canReply"`
}

type RequirementReply struct {
	ID                string `json:"id"`
	IssueID           string `json:"issueId"`
	QuestionCommentID string `json:"questionCommentId"`
	Content           string `json:"content"`
	AuthorID          string `json:"authorId"`
	IssueRevision     int64  `json:"issueRevision"`
}

type RequirementReplyResponse struct {
	Requirement RequirementDetail `json:"requirement"`
	Reply       RequirementReply  `json:"reply"`
}

func (s *RequirementService) ReplyRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input ReplyRequirementRequest, key, requestID string) (RequirementActionResult, error) {
	input.Content = strings.TrimSpace(util.SanitizeTextForPostgres(input.Content))
	questionID, err := util.ParseUUID(input.QuestionCommentID)
	if err != nil || input.ExpectedIssueRevision < 1 || input.Content == "" || len(input.Content) > 20000 {
		return RequirementActionResult{}, requirementBadRequest("requirement_reply_invalid", "a question UUID, issue revision and 1-20000 bytes of content are required")
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "reply", IdempotencyKey: key, Method: http.MethodPost, Route: "POST /api/workspaces/{id}/requirements/{requirementId}/replies",
		ExpectedRevision: &input.ExpectedIssueRevision, Body: input,
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		// Recovery and blocking use Requirement -> Issue -> Wakeup lock order.
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, err
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || len(roles) == 0 {
			return RequirementActionResponse{}, requirementForbidden()
		}
		projection, err := requirementProjection(ctx, q, row, true)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if row.CancelledAt.Valid || projection.State != "blocked" || projection.Issue == nil {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_not_blocked", "requirement has no active blocked question")
		}
		issue := *projection.Issue
		if issue.Revision != input.ExpectedIssueRevision {
			return RequirementActionResponse{}, requirementIssueRevisionConflict(issue.ID, input.ExpectedIssueRevision, issue.Revision)
		}
		wakeups, err := q.ListRequirementIssueWakeups(ctx, db.ListRequirementIssueWakeupsParams{WorkspaceID: actor.WorkspaceID, RequirementID: row.ID})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		var wakeup db.IssueWakeup
		for _, candidate := range wakeups {
			if candidate.ParentCommentID == questionID && candidate.IssueID == issue.ID && !candidate.DisabledAt.Valid {
				wakeup = candidate
				break
			}
		}
		if !wakeup.ID.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_question_changed", "question is no longer active")
		}
		wakeup, err = q.LockIssueWakeup(ctx, wakeup.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if wakeup.DisabledAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_question_changed", "question is no longer active")
		}
		available, err := requirementWakeupTargetAvailable(ctx, q, row, wakeup)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !available || wakeup.FilterActorID != actor.UserID {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('multica.actor_type','member',true),set_config('multica.actor_id',$1,true),set_config('multica.source_task_id','',true)`, util.UUIDToString(actor.UserID)); err != nil {
			return RequirementActionResponse{}, err
		}
		question, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: questionID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_question_changed", "question is unavailable")
		}
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if question.IssueID != issue.ID || question.DeletedAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_question_changed", "question is unavailable")
		}
		created, err := q.CreateComment(ctx, db.CreateCommentParams{ID: dbid.NewV7(), WorkspaceID: actor.WorkspaceID, IssueID: issue.ID, AuthorType: "member", AuthorID: actor.UserID, Content: input.Content, ParentID: questionID, Type: "comment"})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		comment := created.Comment()
		// Record even while the one-shot wakeup is executing. Recovery coalesces
		// uncovered comments after that run; the receipt survives a server restart.
		if !wakeup.Enabled {
			if _, err = recordRequirementCommentReceipt(ctx, q, wakeup, comment); err != nil {
				return RequirementActionResponse{}, err
			}
		}
		if question.ResolvedAt.Valid {
			if _, err = q.UnresolveComment(ctx, questionID); err != nil {
				return RequirementActionResponse{}, err
			}
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", row, "reply", "blocked", "blocked", map[string]any{"comment_id": util.UUIDToString(comment.ID), "question_comment_id": input.QuestionCommentID}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err = s.appendRequirementOutbox(ctx, q, row, event, "requirement_status"); err != nil {
			return RequirementActionResponse{}, err
		}
		detail, err := s.BuildDetail(ctx, q, actor, row, RequirementDetailOptions{ReadAuthorized: true})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		body, err := json.Marshal(RequirementReplyResponse{Requirement: detail, Reply: RequirementReply{ID: util.UUIDToString(comment.ID), IssueID: util.UUIDToString(issue.ID), QuestionCommentID: input.QuestionCommentID, Content: comment.Content, AuthorID: util.UUIDToString(actor.UserID), IssueRevision: created.IssueRevision}})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusCreated, Body: body, EventType: protocol.EventRequirementUpdated, Realtime: detail.RealtimePointer(), Comments: []RequirementCommentEvent{{Issue: issue, Comment: comment, IssueRevision: created.IssueRevision}}}, nil
	})
}

func (s *RequirementService) requirementBlockedQuestion(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, projection requirementView) (*RequirementBlockedQuestion, error) {
	if projection.State != "blocked" || projection.Issue == nil {
		return nil, nil
	}
	rows, err := q.ListRequirementIssueWakeups(ctx, db.ListRequirementIssueWakeupsParams{WorkspaceID: row.WorkspaceID, RequirementID: row.ID})
	if err != nil {
		return nil, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		wakeup := rows[i]
		if wakeup.DisabledAt.Valid || wakeup.IssueID != row.IssueID {
			continue
		}
		question, err := q.GetComment(ctx, wakeup.ParentCommentID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if question.IssueID != row.IssueID || question.WorkspaceID != row.WorkspaceID || question.DeletedAt.Valid {
			continue
		}
		available, err := requirementWakeupTargetAvailable(ctx, q, row, wakeup)
		if err != nil {
			return nil, err
		}
		return &RequirementBlockedQuestion{CommentID: util.UUIDToString(question.ID), WakeupID: util.UUIDToString(wakeup.ID), TargetUserID: util.UUIDToString(wakeup.FilterActorID), AgentID: util.UUIDToString(wakeup.AgentID), Content: question.Content, CanReply: available && wakeup.FilterActorID == actor.UserID}, nil
	}
	return nil, nil
}
