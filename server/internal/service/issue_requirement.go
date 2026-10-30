package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	RequirementApprovalRequiredCode      = "requirement_approval_required"
	RequirementSourceLinkUnavailableCode = "requirement_source_link_unavailable"
)

func requirementSourceLinkUnavailable(message string) *RequirementError {
	return newRequirementError(http.StatusConflict, RequirementSourceLinkUnavailableCode, message)
}

type RequirementApprovalContext struct {
	RequirementID    pgtype.UUID
	ExpectedRevision int64
}

func GateRequirementIssueCreation(ctx context.Context, q *db.Queries, p IssueCreateParams, opts IssueCreateOpts) error {
	if p.Yixiezuo != nil {
		if err := GateRequirementIssueSourceCreation(ctx, q, p.WorkspaceID, p.Yixiezuo.Source); err != nil {
			return err
		}
	}
	if p.OriginType.Valid && p.OriginType.String == "requirement_delegation" {
		requirement, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: p.OriginID, WorkspaceID: p.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode}
		}
		if err != nil {
			return err
		}
		if _, native, err := requirementDelegation(requirement); err != nil || !native {
			return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode}
		}
		if err := requirementCreationGateError(requirement); err != nil {
			return err
		}
		if opts.RequirementApproval == nil || opts.RequirementApproval.RequirementID != requirement.ID {
			return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode, IssueID: util.UUIDToString(requirement.IssueID)}
		}
		return nil
	}
	if p.OriginType.Valid && p.OriginType.String == "requirement_chat" {
		requirement, err := q.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
			WorkspaceID: p.WorkspaceID, ChatSessionID: p.OriginID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode}
		}
		if err != nil {
			return err
		}
		if err := requirementCreationGateError(requirement); err != nil {
			return err
		}
		if opts.RequirementApproval == nil || opts.RequirementApproval.RequirementID != requirement.ID {
			return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode, IssueID: util.UUIDToString(requirement.IssueID)}
		}
		return nil
	}
	if !p.OriginType.Valid || !p.OriginID.Valid {
		return nil
	}
	var requirement db.Requirement
	var err error
	switch p.OriginType.String {
	case "quick_create", "agent_create":
		var task db.AgentTaskQueue
		task, err = q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: p.OriginID, WorkspaceID: p.WorkspaceID})
		if err == nil && !task.ChatSessionID.Valid {
			return nil
		}
		if err == nil && task.ChatSessionID.Valid {
			requirement, err = q.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
				WorkspaceID: p.WorkspaceID, ChatSessionID: task.ChatSessionID,
			})
		}
	default:
		requirement, err = q.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
			WorkspaceID: p.WorkspaceID, ChatSessionID: p.OriginID,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := requirementCreationGateError(requirement); err != nil {
		return err
	}
	if opts.RequirementApproval == nil || opts.RequirementApproval.RequirementID != requirement.ID {
		return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	return nil
}

func GateRequirementIssueSourceCreation(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, source yixiezuo.Source) error {
	requirement, err := q.GetRequirementBySource(ctx, db.GetRequirementBySourceParams{
		WorkspaceID: workspaceID, SourceHost: source.Host, ExternalID: source.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := requirementCreationGateError(requirement); err != nil {
		return err
	}
	return &RequirementChatExecutionError{Code: RequirementApprovalRequiredCode}
}

func requirementCreationGateError(requirement db.Requirement) error {
	if requirement.CancelledAt.Valid {
		return &RequirementChatExecutionError{Code: RequirementCancelledCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	if requirement.IssueID.Valid {
		return &RequirementChatExecutionError{Code: RequirementChatIssueExistsCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	return nil
}

const (
	RequirementStatusTransitionForbiddenCode = "requirement_status_transition_forbidden"
	RequirementIssueNotCancelledCode         = "requirement_issue_not_cancelled"
)

// Explicit requirement issue status actions. The SQL policy migration 9083
// validates every (action, current, next) tuple; a generic issue edit passes
// no action and may not change a requirement-bound issue's status.
const (
	RequirementIssueActionTaskStart    = "task_start"
	RequirementIssueActionTaskComplete = "task_complete"
	RequirementIssueActionAccept       = "accept"
	RequirementIssueActionReject       = "reject"
	RequirementIssueActionBlock        = "block"
	RequirementIssueActionWakeupResume = "wakeup_resume"
	RequirementIssueActionCancel       = "cancel"
	RequirementIssueActionTaskReset    = "task_reset"
)

// RequirementIssueAction builds the explicit requirement-action parameter for
// UpdateIssueParams / UpdateIssueStatusParams. Dedicated requirement actions
// call it before writing so the SQL policy can authorize the transition.
func RequirementIssueAction(action string) pgtype.Text {
	return pgtype.Text{String: action, Valid: action != ""}
}

func UpdateIssueWithRequirementGate(ctx context.Context, q *db.Queries, params db.UpdateIssueParams) (db.Issue, error) {
	var previous db.Issue
	if params.Status.Valid {
		loaded, loadErr := q.GetIssue(ctx, params.ID)
		if loadErr == nil {
			previous = loaded
			if !params.RequirementAction.Valid && params.Status.String == "in_review" && previous.Status != "in_review" {
				action, err := requirementSquadReviewAction(ctx, q, previous, params.SourceTaskID)
				if err != nil {
					return db.Issue{}, err
				}
				params.RequirementAction = action
			}
		} else if !errors.Is(loadErr, pgx.ErrNoRows) {
			return db.Issue{}, loadErr
		}
	}
	issue, err := q.UpdateIssue(ctx, params)
	if !errors.Is(err, pgx.ErrNoRows) {
		if err == nil {
			if transitionErr := syncRequirementOutboxForIssueTransition(ctx, q, previous, issue, params.SourceTaskID); transitionErr != nil {
				return db.Issue{}, transitionErr
			}
		}
		return issue, err
	}
	current, loadErr := q.GetIssue(ctx, params.ID)
	if loadErr != nil || (params.ExpectedRevision.Valid && current.Revision != params.ExpectedRevision.Int64) {
		return issue, err
	}
	if _, requirementErr := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{
		WorkspaceID: current.WorkspaceID, IssueID: current.ID,
	}); requirementErr == nil {
		return db.Issue{}, &RequirementChatExecutionError{Code: RequirementStatusTransitionForbiddenCode, IssueID: util.UUIDToString(current.ID)}
	}
	return issue, err
}

func UpdateIssueStatusWithRequirementGate(ctx context.Context, q *db.Queries, params db.UpdateIssueStatusParams) (db.Issue, error) {
	var previous db.Issue
	loaded, loadErr := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: params.ID, WorkspaceID: params.WorkspaceID})
	if loadErr == nil {
		previous = loaded
	} else if !errors.Is(loadErr, pgx.ErrNoRows) {
		return db.Issue{}, loadErr
	}
	issue, err := q.UpdateIssueStatus(ctx, params)
	if !errors.Is(err, pgx.ErrNoRows) {
		if err == nil {
			if transitionErr := syncRequirementOutboxForIssueTransition(ctx, q, previous, issue, params.SourceTaskID); transitionErr != nil {
				return db.Issue{}, transitionErr
			}
		}
		if err == nil && issue.Status != "blocked" {
			if _, disableErr := disableRequirementWakeupsForIssue(ctx, q, issue.WorkspaceID, issue.ID); disableErr != nil {
				return db.Issue{}, disableErr
			}
		}
		return issue, err
	}
	current, loadErr := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: params.ID, WorkspaceID: params.WorkspaceID})
	if loadErr != nil {
		return issue, err
	}
	if _, requirementErr := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{
		WorkspaceID: current.WorkspaceID, IssueID: current.ID,
	}); requirementErr == nil {
		return db.Issue{}, &RequirementChatExecutionError{Code: RequirementStatusTransitionForbiddenCode, IssueID: util.UUIDToString(current.ID)}
	}
	return issue, err
}

func GateRequirementIssueDeletion(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID) error {
	requirement, err := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: workspaceID, IssueID: issueID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !requirement.CancelledAt.Valid {
		return &RequirementChatExecutionError{Code: RequirementIssueNotCancelledCode, IssueID: util.UUIDToString(issueID)}
	}
	return nil
}
