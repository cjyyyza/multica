package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func validateRequirementExecuteAgent(ctx context.Context, q *db.Queries, requirement db.Requirement, task db.AgentTaskQueue) error {
	source, native, err := requirementDelegation(requirement)
	if err != nil {
		return err
	}
	if !native || source.SquadID == "" {
		if task.AgentID != requirement.AgentID {
			return taskRequirementBindingErrorf("task agent does not match its requirement")
		}
		return nil
	}
	squadID := util.MustParseUUID(source.SquadID)
	squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: requirement.WorkspaceID})
	if err != nil {
		return err
	}
	if squad.ArchivedAt.Valid || squad.LeaderID != requirement.AgentID {
		return taskRequirementBindingErrorf("approved requirement squad is no longer available")
	}
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: requirement.IssueID, WorkspaceID: requirement.WorkspaceID})
	if err != nil {
		return err
	}
	if issue.AssigneeType.String != "squad" || issue.AssigneeID != squadID {
		return taskRequirementBindingErrorf("requirement issue no longer belongs to its approved squad")
	}
	owner, err := q.GetEffectiveSquadConcurrencyForTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if owner.SquadID != squadID {
		return taskRequirementBindingErrorf("task belongs to another squad")
	}
	if task.IsLeaderTask {
		if task.AgentID != requirement.AgentID || task.SquadID != squadID {
			return taskRequirementBindingErrorf("task is not the approved squad leader")
		}
		return nil
	}
	member, err := q.IsSquadMember(ctx, db.IsSquadMemberParams{SquadID: squadID, MemberType: "agent", MemberID: task.AgentID})
	if err != nil {
		return err
	}
	if member {
		return nil
	}
	// Guest workers need actual delegation from this requirement's squad;
	// issue assignment alone must not authorize an unrelated agent.
	seen := map[pgtype.UUID]bool{task.ID: true}
	for parentID := task.DelegatedFromTaskID; parentID.Valid && !seen[parentID]; {
		seen[parentID] = true
		parent, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: parentID, WorkspaceID: requirement.WorkspaceID})
		if err != nil {
			return err
		}
		if parent.RequirementID != requirement.ID || parent.IssueID != requirement.IssueID || parent.Mode != "execute" {
			break
		}
		if parent.IsLeaderTask {
			if parent.AgentID == requirement.AgentID && parent.SquadID == squadID {
				return nil
			}
			break
		}
		parentID = parent.DelegatedFromTaskID
	}
	return taskRequirementBindingErrorf("task agent is neither an approved squad member nor its delegated worker")
}

func requirementSquadReviewAction(ctx context.Context, q *db.Queries, issue db.Issue, sourceTaskID pgtype.UUID) (pgtype.Text, error) {
	if !sourceTaskID.Valid {
		return pgtype.Text{}, nil
	}
	requirement, err := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Text{}, nil
	}
	if err != nil {
		return pgtype.Text{}, err
	}
	source, native, err := requirementDelegation(requirement)
	if err != nil || !native || source.SquadID == "" {
		return pgtype.Text{}, err
	}
	forbidden := &RequirementChatExecutionError{Code: RequirementStatusTransitionForbiddenCode, IssueID: util.UUIDToString(issue.ID)}
	task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: sourceTaskID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return pgtype.Text{}, err
	}
	if requirement.CancelledAt.Valid || task.Status != "running" || task.Mode != "execute" || !task.IsLeaderTask || task.RequirementID != requirement.ID || task.IssueID != issue.ID {
		return pgtype.Text{}, forbidden
	}
	if err := validateRequirementExecuteAgent(ctx, q, requirement, task); err != nil {
		if errors.Is(err, ErrTaskRequirementBinding) || errors.Is(err, pgx.ErrNoRows) {
			return pgtype.Text{}, forbidden
		}
		return pgtype.Text{}, err
	}
	pending, err := q.HasPendingRequirementDelegationWork(ctx, db.HasPendingRequirementDelegationWorkParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, SourceTaskID: sourceTaskID})
	if err != nil {
		return pgtype.Text{}, err
	}
	if pending {
		return pgtype.Text{}, forbidden
	}
	return RequirementIssueAction(RequirementIssueActionTaskComplete), nil
}
