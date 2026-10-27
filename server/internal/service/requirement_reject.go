package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const requirementRejectRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/reject"

func (s *RequirementService) RejectRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input RejectRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.ExpectedIssueRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_issue_revision_invalid", "expectedIssueRevision must be positive")
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		return RequirementActionResult{}, requirementBadRequest("requirement_reject_reason_required", "reason is required")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement reject: task service is required")
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "reject", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementRejectRoute,
		ExpectedRevision: &input.ExpectedIssueRevision, Body: input,
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, issue, err := s.lockRequirementReviewAction(ctx, q, actor, requirementID, input.ExpectedIssueRevision)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		created, err := q.CreateComment(ctx, db.CreateCommentParams{
			ID: dbid.NewV7(), IssueID: issue.ID, WorkspaceID: actor.WorkspaceID,
			AuthorType: "member", AuthorID: actor.UserID, Content: input.Reason, Type: "comment",
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement rejection comment: %w", err)
		}
		rejectParams := requirementIssueUpdateParams(issue, created.IssueRevision, "in_progress")
		rejectParams.RequirementAction = RequirementIssueAction(RequirementIssueActionReject)
		updatedIssue, err := UpdateIssueWithRequirementGate(ctx, q, rejectParams)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		cancelledTasks, err := disableRequirementWakeupsForIssue(ctx, q, actor.WorkspaceID, updatedIssue.ID)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("disable rejected requirement wakeups: %w", err)
		}
		task, err := s.Tasks.createDeferredChannelIssueTaskWithQueries(ctx, q, updatedIssue, time.Now().Add(time.Minute))
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement rejection task: %w", err)
		}
		if task.RequirementID != row.ID || task.Mode != "execute" || task.IssueID != updatedIssue.ID {
			return RequirementActionResponse{}, errors.New("requirement reject: task binding is invalid")
		}
		updatedRequirement, err := q.AdvanceRequirementRevision(ctx, db.AdvanceRequirementRevisionParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: row.Revision,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("advance rejected requirement revision: %w", err)
		}
		comment := created.Comment()
		event, err := appendRequirementEvent(ctx, q, actor, "member", updatedRequirement, "rejected", "in_review", "executing", map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "issue_id": util.UUIDToString(issue.ID),
			"issue_revision": updatedIssue.Revision, "comment_id": util.UUIDToString(comment.ID), "task_id": util.UUIDToString(task.ID),
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutboxWithOptions(ctx, q, updatedRequirement, event, "requirement_status", RequirementOutboxOptions{
			IssueRevision: pgtype.Int8{Int64: updatedIssue.Revision, Valid: true},
		}); err != nil {
			return RequirementActionResponse{}, err
		}
		return s.requirementActionDetailResponse(ctx, q, actor, updatedRequirement, RequirementActionResponse{
			PromoteTasks:   []db.AgentTaskQueue{task},
			IssueUpdates:   []RequirementIssueUpdate{{Issue: updatedIssue, PreviousStatus: issue.Status}},
			Comments:       []RequirementCommentEvent{{Issue: updatedIssue, Comment: comment, IssueRevision: updatedIssue.Revision}},
			CancelledTasks: cancelledTasks,
		})
	})
}
