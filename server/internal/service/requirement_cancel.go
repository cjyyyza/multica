package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const requirementCancelRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/cancel"

func (s *RequirementService) CancelRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input CancelRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.ExpectedRevision != nil && *input.ExpectedRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_invalid", "expectedRevision must be positive")
	}
	if input.ExpectedIssueRevision != nil && *input.ExpectedIssueRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_issue_revision_invalid", "expectedIssueRevision must be positive")
	}
	if input.ExpectedRevision == nil && input.ExpectedIssueRevision == nil {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_required", "an expected revision is required")
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement cancel: task service is required")
	}
	expectedActionRevision := input.ExpectedRevision
	if expectedActionRevision == nil {
		expectedActionRevision = input.ExpectedIssueRevision
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "cancel", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementCancelRoute,
		ExpectedRevision: expectedActionRevision, Body: input,
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement cancel: %w", err)
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || (!roles["requester"] && !roles["approver"]) {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if row.CancelledAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is already cancelled")
		}
		if input.ExpectedRevision != nil && row.Revision != *input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, *input.ExpectedRevision, row.Revision)
		}
		previousState := row.IntakeState
		response := RequirementActionResponse{}
		var issueRevision pgtype.Int8
		if row.IssueID.Valid {
			if input.ExpectedIssueRevision == nil {
				return RequirementActionResponse{}, requirementBadRequest("expected_issue_revision_required", "expectedIssueRevision is required when a requirement has an issue")
			}
			projection, err := s.requirementProjection(ctx, q, row, true)
			if err != nil {
				return RequirementActionResponse{}, err
			}
			if projection.Issue == nil {
				return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementIssueRequiredCode, "requirement issue is unavailable")
			}
			if projection.State == "done" {
				return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementDoneCode, "requirement is done")
			}
			if projection.State == "cancelled" {
				return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is already cancelled")
			}
			previousState = projection.State
			issue := *projection.Issue
			if issue.Revision != *input.ExpectedIssueRevision {
				return RequirementActionResponse{}, requirementIssueRevisionConflict(issue.ID, *input.ExpectedIssueRevision, issue.Revision)
			}
			cancelParams := requirementIssueUpdateParams(issue, *input.ExpectedIssueRevision, "cancelled")
			cancelParams.RequirementAction = RequirementIssueAction(RequirementIssueActionCancel)
			updatedIssue, err := UpdateIssueWithRequirementGate(ctx, q, cancelParams)
			if err != nil {
				return RequirementActionResponse{}, err
			}
			cancelledWakeups, err := stopRequirementIssueWakeups(ctx, q, updatedIssue)
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("stop cancelled requirement wakeups: %w", err)
			}
			cancelledTasks, err := s.Tasks.CancelTasksForIssueInTx(ctx, tx, updatedIssue.ID)
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("cancel requirement tasks: %w", err)
			}
			response.IssueUpdates = []RequirementIssueUpdate{{Issue: updatedIssue, PreviousStatus: issue.Status}}
			response.CancelledTasks = append(cancelledWakeups, cancelledTasks...)
			issueRevision = pgtype.Int8{Int64: updatedIssue.Revision, Valid: true}
			if err := q.FencePendingYixiezuoOperationsForIssue(ctx, db.FencePendingYixiezuoOperationsForIssueParams{
				WorkspaceID: actor.WorkspaceID, IssueID: updatedIssue.ID,
			}); err != nil {
				return RequirementActionResponse{}, fmt.Errorf("fence requirement writeback: %w", err)
			}
		} else {
			if input.ExpectedRevision == nil {
				return RequirementActionResponse{}, requirementBadRequest("expected_revision_required", "expectedRevision is required when a requirement has no issue")
			}
			if input.ExpectedIssueRevision != nil {
				return RequirementActionResponse{}, requirementBadRequest("requirement_issue_required", "expectedIssueRevision cannot be used before requirement approval")
			}
			if row.ChatSessionID.Valid {
				if _, err := q.LockChatSessionForDelete(ctx, row.ChatSessionID); err != nil {
					return RequirementActionResponse{}, fmt.Errorf("lock requirement chat for cancel: %w", err)
				}
				if _, err := q.GetAgentForClaimUpdate(ctx, row.AgentID); err != nil {
					return RequirementActionResponse{}, fmt.Errorf("lock requirement chat agent for cancel: %w", err)
				}
				cancelledTasks, err := q.CancelAgentTasksByChatSession(ctx, row.ChatSessionID)
				if err != nil {
					return RequirementActionResponse{}, fmt.Errorf("cancel requirement chat tasks: %w", err)
				}
				if err := SettleTerminalTaskState(ctx, q, cancelledTasks...); err != nil {
					return RequirementActionResponse{}, fmt.Errorf("settle requirement chat tasks: %w", err)
				}
				response.CancelledTasks = append(response.CancelledTasks, cancelledTasks...)
			}
			if row.IntakeOperationID.Valid {
				if err := q.FencePendingYixiezuoOperation(ctx, db.FencePendingYixiezuoOperationParams{
					WorkspaceID: actor.WorkspaceID, OperationID: row.IntakeOperationID,
				}); err != nil {
					return RequirementActionResponse{}, fmt.Errorf("fence requirement intake: %w", err)
				}
			}
		}
		updatedRequirement, err := q.CancelRequirement(ctx, db.CancelRequirementParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: row.Revision,
			CancelReason: pgtype.Text{String: input.Reason, Valid: input.Reason != ""},
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("cancel requirement: %w", err)
		}
		if err := supersedeRequirementOutbox(ctx, q, actor.WorkspaceID, row.ID, "", pgtype.UUID{}); err != nil {
			return RequirementActionResponse{}, fmt.Errorf("supersede requirement outbox: %w", err)
		}
		refs := map[string]any{"requirement_id": util.UUIDToString(row.ID)}
		if input.Reason != "" {
			refs["reason"] = input.Reason
		}
		if issueRevision.Valid {
			refs["issue_revision"] = issueRevision.Int64
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", updatedRequirement, "cancelled", previousState, "cancelled", refs, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutboxWithOptions(ctx, q, updatedRequirement, event, "requirement_status", RequirementOutboxOptions{IssueRevision: issueRevision}); err != nil {
			return RequirementActionResponse{}, err
		}
		return s.requirementActionDetailResponse(ctx, q, actor, updatedRequirement, response)
	})
}
