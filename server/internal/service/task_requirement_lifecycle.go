package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func requirementTaskIssue(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (db.Requirement, db.Issue, bool, error) {
	if !task.RequirementID.Valid || !task.IssueID.Valid || task.Mode != "execute" {
		return db.Requirement{}, db.Issue{}, false, nil
	}
	issue, err := q.GetIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Requirement{}, db.Issue{}, false, nil
	}
	if err != nil {
		return db.Requirement{}, db.Issue{}, false, err
	}
	requirement, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: task.RequirementID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Requirement{}, db.Issue{}, false, fmt.Errorf("%w: requirement task binding is missing", ErrTaskRequirementBinding)
	}
	if err != nil {
		return db.Requirement{}, db.Issue{}, false, err
	}
	if requirement.IssueID != issue.ID {
		return db.Requirement{}, db.Issue{}, false, fmt.Errorf("%w: requirement task issue binding is inconsistent", ErrTaskRequirementBinding)
	}
	return requirement, issue, true, nil
}

func requirementIssueIsTerminal(ctx context.Context, q *db.Queries, issue db.Issue) (bool, error) {
	if issue.Status == "done" || issue.Status == "cancelled" {
		return true, nil
	}
	if requirementBuiltinIssueStatus(issue.Status) {
		return false, nil
	}
	status, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{WorkspaceID: issue.WorkspaceID, Key: issue.Status})
	if err != nil {
		return false, err
	}
	return status.Category == "done" || status.Category == "closed", nil
}

func validateRequirementWakeupResumeTask(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, requirement db.Requirement, issue db.Issue) error {
	stored, ok := parseRequirementWakeupTaskContext(task)
	if !ok || !task.TriggerCommentID.Valid {
		return fmt.Errorf("%w: blocked task has no requirement wakeup answer", ErrTaskRequirementBinding)
	}
	wakeupID := util.MustParseUUID(stored.WakeupID)
	wakeup, err := q.GetIssueWakeup(ctx, db.GetIssueWakeupParams{ID: wakeupID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return fmt.Errorf("%w: requirement wakeup is unavailable", ErrTaskRequirementBinding)
	}
	if wakeup.RequirementID != requirement.ID || wakeup.IssueID != issue.ID || wakeup.DisabledAt.Valid ||
		wakeup.Revision != stored.Generation || wakeup.ParentCommentID != util.MustParseUUID(stored.QuestionCommentID) ||
		wakeup.LastTriggeredCommentID != task.TriggerCommentID || wakeup.LastTaskID != task.ID {
		return fmt.Errorf("%w: requirement wakeup answer does not own this task", ErrTaskRequirementBinding)
	}
	return nil
}

func updateRequirementTaskIssue(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, action, nextStatus string) (*RequirementIssueUpdate, error) {
	requirement, issue, ok, err := requirementTaskIssue(ctx, q, task)
	if err != nil || !ok {
		return nil, err
	}
	if requirement.CancelledAt.Valid {
		if action == RequirementIssueActionTaskComplete {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: requirement task issue is cancelled", ErrTaskRequirementBinding)
	}
	terminal, err := requirementIssueIsTerminal(ctx, q, issue)
	if err != nil {
		return nil, err
	}
	if action == RequirementIssueActionTaskComplete && (terminal || issue.Status == "blocked") {
		return nil, nil
	}
	if issue.Status == nextStatus {
		return nil, nil
	}
	if action == RequirementIssueActionWakeupResume {
		if err := validateRequirementWakeupResumeTask(ctx, q, task, requirement, issue); err != nil {
			return nil, err
		}
	}
	updated, err := UpdateIssueStatusWithRequirementGate(ctx, q, db.UpdateIssueStatusParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: nextStatus,
		SourceTaskID: task.ID, RequirementAction: RequirementIssueAction(action),
	})
	if err != nil {
		return nil, err
	}
	return &RequirementIssueUpdate{Issue: updated, PreviousStatus: issue.Status}, nil
}

func advanceRequirementTaskStart(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (*RequirementIssueUpdate, error) {
	_, issue, ok, err := requirementTaskIssue(ctx, q, task)
	if err != nil || !ok {
		return nil, err
	}
	action := RequirementIssueActionTaskStart
	if issue.Status == "blocked" {
		action = RequirementIssueActionWakeupResume
	}
	return updateRequirementTaskIssue(ctx, q, task, action, "in_progress")
}

func advanceRequirementTaskComplete(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (*RequirementIssueUpdate, error) {
	requirement, _, ok, err := requirementTaskIssue(ctx, q, task)
	if err != nil || !ok {
		return nil, err
	}
	if source, native, err := requirementDelegation(requirement); err != nil || (native && source.SquadID != "") {
		// A squad turn can be dispatch or partial delivery. Its leader submits
		// the aggregate through the guarded issue-status command.
		return nil, err
	}
	return updateRequirementTaskIssue(ctx, q, task, RequirementIssueActionTaskComplete, "in_review")
}

func broadcastRequirementTaskIssueUpdate(ctx context.Context, s *TaskService, update *RequirementIssueUpdate) {
	if update == nil {
		return
	}
	s.broadcastIssueUpdated(ctx, update.Issue, update.PreviousStatus)
}
