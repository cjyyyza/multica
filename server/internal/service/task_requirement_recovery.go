package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type TerminalTaskSettlement struct {
	RequirementRecoveryTasks []db.AgentTaskQueue
	activities               []wakeupActivity
}

// SettleTerminalTaskStateWithRecovery also re-arms a completed requirement
// wakeup in place, creating a run only when canonical comments are not covered.
// Returned tasks are finalized only after commit.
func SettleTerminalTaskStateWithRecovery(ctx context.Context, tx pgx.Tx, q *db.Queries, tasks ...db.AgentTaskQueue) (TerminalTaskSettlement, error) {
	return SettleTerminalTaskStateWithRecoveryOptions(ctx, tx, q, false, tasks...)
}

func SettleTerminalTaskStateWithRecoveryOptions(ctx context.Context, tx pgx.Tx, q *db.Queries, suppressRequirementRecovery bool, tasks ...db.AgentTaskQueue) (TerminalTaskSettlement, error) {
	var settlement TerminalTaskSettlement
	if err := SettleTerminalTaskState(ctx, q, tasks...); err != nil {
		return settlement, err
	}
	if suppressRequirementRecovery {
		return settlement, nil
	}
	wakeups := &IssueWakeupService{Tasks: &TaskService{Queries: q}}
	for _, task := range tasks {
		stored, ok := parseRequirementWakeupTaskContext(task)
		if !ok {
			continue
		}
		if tx == nil {
			return settlement, errors.New("requirement wakeup recovery requires a transaction")
		}
		issue, err := q.GetIssue(ctx, task.IssueID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return settlement, err
		}
		result, err := wakeups.rearmRequirementWakeup(ctx, q, issue.WorkspaceID, util.MustParseUUID(stored.WakeupID), stored.Generation, task.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return settlement, err
		}
		if result.Task != nil {
			settlement.RequirementRecoveryTasks = append(settlement.RequirementRecoveryTasks, *result.Task)
		}
		settlement.activities = append(settlement.activities, result.Activities...)
	}
	return settlement, nil
}

func (s *TaskService) FinalizeTerminalTaskSettlement(ctx context.Context, settlement TerminalTaskSettlement) {
	(&IssueWakeupService{Tasks: s}).publishWakeupActivities(settlement.activities...)
	for _, task := range settlement.RequirementRecoveryTasks {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		s.NotifyTaskEnqueued(ctx, task)
	}
}
