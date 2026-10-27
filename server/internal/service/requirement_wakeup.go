package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// disableRequirementWakeupsForIssue fences every question wakeup in place when
// an Issue leaves blocked or is deleted after cancellation. A question is one
// issue_wakeup row, so one fence also stops its receipts and unstarted runs.
func disableRequirementWakeupsForIssue(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	row, err := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: workspaceID, IssueID: issueID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRequirementIssueWakeups(ctx, db.ListRequirementIssueWakeupsParams{WorkspaceID: workspaceID, RequirementID: row.ID})
	if err != nil {
		return nil, err
	}
	if err := supersedeRequirementOutbox(ctx, q, workspaceID, row.ID, "requirement_blocked", pgtype.UUID{}); err != nil {
		return nil, err
	}
	var cancelled []db.AgentTaskQueue
	for _, wakeup := range rows {
		if wakeup.DisabledAt.Valid {
			continue
		}
		if err := q.DisableIssueWakeupByID(ctx, wakeup.ID); err != nil {
			return nil, err
		}
		if err := q.DiscardWakeupReceipts(ctx, wakeup.ID); err != nil {
			return nil, err
		}
		tasks, err := q.CancelUnstartedWakeupTasks(ctx, util.UUIDToString(wakeup.ID))
		if err != nil {
			return nil, err
		}
		if err := SettleTerminalTaskState(ctx, q, tasks...); err != nil {
			return nil, err
		}
		cancelled = append(cancelled, tasks...)
	}
	return cancelled, nil
}

// DisableRequirementWakeupsForIssue fences every question wakeup when an Issue
// leaves blocked or is deleted after cancellation.
func DisableRequirementWakeupsForIssue(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return disableRequirementWakeupsForIssue(ctx, q, workspaceID, issueID)
}

// requirementWakeupState reports the lifecycle state of a stable requirement
// wakeup: armed waits for an answer, triggered woke once and waits for recovery,
// disabled is fenced for good.
func requirementWakeupState(wakeup db.IssueWakeup) string {
	switch {
	case wakeup.DisabledAt.Valid:
		return "disabled"
	case wakeup.Enabled:
		return "armed"
	default:
		return "triggered"
	}
}
