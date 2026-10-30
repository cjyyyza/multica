package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

const (
	RequirementTaskScopeCode        = "requirement_task_scope"
	RequirementTargetNotParticipant = "requirement_target_not_participant"
	RequirementBlockTransitionCode  = "requirement_block_transition_forbidden"
)

type BlockRequirementRequest struct {
	Question              string `json:"question"`
	TargetUserID          string `json:"targetUserId"`
	ExpectedIssueRevision int64  `json:"expectedIssueRevision"`
}

func (s *RequirementService) BlockRequirement(ctx context.Context, actor RequirementActor, taskID, requirementID pgtype.UUID, input BlockRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	input.Question = strings.TrimSpace(util.SanitizeTextForPostgres(input.Question))
	input.TargetUserID = strings.TrimSpace(input.TargetUserID)
	if input.Question == "" || len(input.Question) > 12000 {
		return RequirementActionResult{}, requirementBadRequest("requirement_question_invalid", "question must contain 1–12000 bytes")
	}
	target, err := util.ParseUUID(input.TargetUserID)
	if err != nil {
		return RequirementActionResult{}, requirementBadRequest("requirement_target_invalid", "targetUserId must be a UUID")
	}
	input.TargetUserID = util.UUIDToString(target)
	if input.ExpectedIssueRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_issue_revision_invalid", "expectedIssueRevision must be positive")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement block: task service is required")
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "agent", ActorID: actor.UserID,
		Action: "block", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: taskpolicy.RequirementBlockRoute,
		ExpectedRevision: &input.ExpectedIssueRevision, Body: input,
		AuthorizeReplay: func(ctx context.Context, q *db.Queries, requirementID pgtype.UUID) error {
			row, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
			if err != nil {
				return requirementNotFound()
			}
			return validateRequirementExecuteTask(ctx, q, actor, row, taskID)
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, issue, _, err := s.lockRequirementBlockAction(ctx, q, actor, taskID, requirementID, input.ExpectedIssueRevision)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		roles, err := q.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID, UserID: target,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement block target: %w", err)
		}
		if len(roles) == 0 {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, RequirementTargetNotParticipant, "target user is not a requirement participant")
		}
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: target, WorkspaceID: actor.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, RequirementTargetNotParticipant, "target user is not a current workspace member")
		} else if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement block target member: %w", err)
		}

		created, err := q.CreateComment(ctx, db.CreateCommentParams{
			ID: dbid.NewV7(), IssueID: issue.ID, WorkspaceID: actor.WorkspaceID,
			AuthorType: "agent", AuthorID: actor.UserID, Content: input.Question, Type: "comment", SourceTaskID: taskID,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement question comment: %w", err)
		}
		blockParams := requirementIssueUpdateParams(issue, created.IssueRevision, "blocked")
		blockParams.RequirementAction = RequirementIssueAction(RequirementIssueActionBlock)
		updatedIssue, err := UpdateIssueWithRequirementGate(ctx, q, blockParams)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		cancelledTasks, err := disableRequirementWakeupsForIssue(ctx, q, actor.WorkspaceID, issue.ID)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("disable previous requirement wakeups: %w", err)
		}

		var activities []wakeupActivity
		wakeup, err := (&IssueWakeupService{Tasks: s.Tasks}).saveWakeupInTx(ctx, tx, issue.ID, target, taskID, pgtype.UUID{}, row.ID, WakeupInput{
			AgentID: util.UUIDToString(actor.UserID), Instruction: input.Question,
			Kind: "event", Mode: "once", EventTypes: []string{"comment.created"},
			FilterActorType: "member", FilterActorID: util.UUIDToString(target), ParentCommentID: util.UUIDToString(created.ID),
		}, nil, &activities)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement question wakeup: %w", err)
		}
		event, err := appendRequirementEvent(ctx, q, actor, "agent", row, "blocked", issue.Status, "blocked", map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "issue_id": util.UUIDToString(issue.ID), "task_id": util.UUIDToString(taskID),
			"question_comment_id": util.UUIDToString(created.ID), "target_user_id": util.UUIDToString(target),
			"wakeup_id": util.UUIDToString(wakeup.ID), "wakeup_revision": wakeup.Revision,
			"issue_revision": updatedIssue.Revision,
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutboxWithOptions(ctx, q, row, event, "requirement_blocked", RequirementOutboxOptions{
			OccurrenceID: created.ID, IssueRevision: pgtype.Int8{Int64: updatedIssue.Revision, Valid: true}, RecipientUserID: target,
		}); err != nil {
			return RequirementActionResponse{}, err
		}
		comment := created.Comment()
		response, err := s.requirementActionDetailResponse(ctx, q, actor, row, RequirementActionResponse{
			IssueUpdates:   []RequirementIssueUpdate{{Issue: updatedIssue, PreviousStatus: issue.Status}},
			Comments:       []RequirementCommentEvent{{Issue: updatedIssue, Comment: comment, IssueRevision: updatedIssue.Revision}},
			CancelledTasks: cancelledTasks,
			PostCommit:     func() { (&IssueWakeupService{Tasks: s.Tasks}).publishWakeupActivities(activities...) },
		})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		commentBody := commentEventFields(comment)
		commentBody["issue_revision"] = updatedIssue.Revision
		response.Body, err = json.Marshal(map[string]any{
			"requirement":      response.Body,
			"question_comment": commentBody,
			"wakeup": map[string]any{
				"id": util.UUIDToString(wakeup.ID), "question_comment_id": util.UUIDToString(wakeup.ParentCommentID),
				"revision": wakeup.Revision, "generation": wakeup.Revision, "state": requirementWakeupState(wakeup),
			},
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement block response: %w", err)
		}
		return response, nil
	})
}

func (s *RequirementService) lockRequirementBlockAction(ctx context.Context, q *db.Queries, actor RequirementActor, taskID, requirementID pgtype.UUID, expectedIssueRevision int64) (db.Requirement, db.Issue, db.AgentTaskQueue, error) {
	row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, requirementNotFound()
	}
	if err != nil {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, fmt.Errorf("lock requirement block: %w", err)
	}
	if row.CancelledAt.Valid {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is cancelled")
	}
	if !row.IssueID.Valid {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, newRequirementError(http.StatusConflict, RequirementIssueRequiredCode, "requirement has no issue")
	}
	if err := validateRequirementExecuteTask(ctx, q, actor, row, taskID); err != nil {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, err
	}
	projection, err := s.requirementProjection(ctx, q, row, true)
	if err != nil {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, err
	}
	if projection.Issue == nil {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, newRequirementError(http.StatusConflict, RequirementIssueRequiredCode, "requirement issue is unavailable")
	}
	if projection.State != "executing" && projection.State != "blocked" {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, newRequirementError(http.StatusConflict, RequirementBlockTransitionCode, "requirement is not executing")
	}
	issue := *projection.Issue
	if issue.Revision != expectedIssueRevision {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, requirementIssueRevisionConflict(issue.ID, expectedIssueRevision, issue.Revision)
	}
	task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: actor.WorkspaceID})
	if err != nil {
		return db.Requirement{}, db.Issue{}, db.AgentTaskQueue{}, requirementForbidden()
	}
	return row, issue, task, nil
}

func validateRequirementExecuteTask(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, taskID pgtype.UUID) error {
	task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return newRequirementError(http.StatusForbidden, RequirementTaskScopeCode, "task does not belong to this requirement")
	}
	if err != nil {
		return fmt.Errorf("load requirement block task: %w", err)
	}
	if task.AgentID != actor.UserID || task.RequirementID != row.ID || task.IssueID != row.IssueID || task.Mode != "execute" || task.Status != "running" {
		return newRequirementError(http.StatusForbidden, RequirementTaskScopeCode, "task is not a running execute task for this requirement issue")
	}
	if err := validateRequirementExecuteAgent(ctx, q, row, task); err != nil {
		if errors.Is(err, ErrTaskRequirementBinding) || errors.Is(err, pgx.ErrNoRows) {
			return newRequirementError(http.StatusForbidden, RequirementTaskScopeCode, "task does not belong to the approved requirement squad")
		}
		return err
	}
	return nil
}
