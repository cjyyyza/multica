package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// requirementWakeupTaskContext names the one stable wakeup row that owns a
// requirement run. requirement_wakeup_generation is its revision, not a second
// physical successor.
type requirementWakeupTaskContext struct {
	WakeupID          string `json:"wakeup_id"`
	QuestionCommentID string `json:"requirement_question_comment_id"`
	Generation        int64  `json:"requirement_wakeup_generation"`
}

func parseRequirementWakeupTaskContext(task db.AgentTaskQueue) (requirementWakeupTaskContext, bool) {
	var stored requirementWakeupTaskContext
	if json.Unmarshal(task.Context, &stored) != nil || stored.WakeupID == "" || stored.QuestionCommentID == "" || stored.Generation < 1 {
		return requirementWakeupTaskContext{}, false
	}
	if _, err := util.ParseUUID(stored.WakeupID); err != nil {
		return requirementWakeupTaskContext{}, false
	}
	if _, err := util.ParseUUID(stored.QuestionCommentID); err != nil {
		return requirementWakeupTaskContext{}, false
	}
	return stored, true
}

// requirementForWakeup resolves the Requirement that owns an issue wakeup. It
// returns nil for an ordinary wakeup and pgx.ErrNoRows when the wakeup claims a
// requirement that no longer belongs to the same issue.
func requirementForWakeup(ctx context.Context, q *db.Queries, wakeup db.IssueWakeup) (*db.Requirement, error) {
	if !wakeup.RequirementID.Valid {
		return nil, nil
	}
	row, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: wakeup.RequirementID, WorkspaceID: wakeup.WorkspaceID})
	if err != nil {
		return nil, err
	}
	if !row.IssueID.Valid || row.IssueID != wakeup.IssueID {
		return nil, pgx.ErrNoRows
	}
	return &row, nil
}

func requirementWakeupTargetAvailable(ctx context.Context, q *db.Queries, row db.Requirement, wakeup db.IssueWakeup) (bool, error) {
	if wakeup.FilterActorType.String != "member" || !wakeup.FilterActorID.Valid || wakeup.CreatedBy != wakeup.FilterActorID {
		return false, nil
	}
	roles, err := q.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
		WorkspaceID: wakeup.WorkspaceID, RequirementID: row.ID, UserID: wakeup.FilterActorID,
	})
	if err != nil || len(roles) == 0 {
		return false, err
	}
	_, err = q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: wakeup.FilterActorID, WorkspaceID: wakeup.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RequirementCommentRecoveryOwner identifies the agent whose requirement
// recovery owns this exact target comment, including after a one-shot question
// wakeup has fired. The stable wakeup row, not receipt lifetime, owns
// suppression.
func RequirementCommentRecoveryOwner(ctx context.Context, q *db.Queries, issue db.Issue, comment db.Comment) (pgtype.UUID, bool, error) {
	if comment.AuthorType != "member" || issue.Status != "blocked" {
		return pgtype.UUID{}, false, nil
	}
	row, err := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, false, nil
	}
	if err != nil {
		return pgtype.UUID{}, false, err
	}
	if row.CancelledAt.Valid {
		return pgtype.UUID{}, false, nil
	}
	rows, err := q.ListRequirementIssueWakeups(ctx, db.ListRequirementIssueWakeupsParams{WorkspaceID: issue.WorkspaceID, RequirementID: row.ID})
	if err != nil {
		return pgtype.UUID{}, false, err
	}
	for _, wakeup := range rows {
		if wakeup.DisabledAt.Valid {
			continue
		}
		if wakeup.FilterActorType.String == "member" && wakeup.FilterActorID == comment.AuthorID {
			return wakeup.AgentID, true, nil
		}
	}
	return pgtype.UUID{}, false, nil
}

func uncoveredRequirementComments(ctx context.Context, q *db.Queries, wakeup db.IssueWakeup) ([]db.Comment, error) {
	comments, err := q.ListRequirementTargetComments(ctx, db.ListRequirementTargetCommentsParams{
		WorkspaceID: wakeup.WorkspaceID, IssueID: wakeup.IssueID,
		QuestionCommentID: wakeup.ParentCommentID, TargetUserID: wakeup.FilterActorID,
	})
	if err != nil {
		return nil, err
	}
	coverageRows, err := q.ListRequirementCommentCoverage(ctx, db.ListRequirementCommentCoverageParams{
		IssueID: wakeup.IssueID, RequirementID: wakeup.RequirementID, QuestionCommentID: util.UUIDToString(wakeup.ParentCommentID),
	})
	if err != nil {
		return nil, err
	}
	covered := make(map[pgtype.UUID]bool)
	if wakeup.LastTriggeredCommentID.Valid {
		covered[wakeup.LastTriggeredCommentID] = true
	}
	for _, row := range coverageRows {
		if row.TriggerCommentID.Valid {
			covered[row.TriggerCommentID] = true
		}
		for _, id := range row.CoalescedCommentIds {
			covered[id] = true
		}
		for _, id := range row.DeliveredCommentIds {
			covered[id] = true
		}
	}
	uncovered := make([]db.Comment, 0, len(comments))
	for _, comment := range comments {
		if !covered[comment.ID] {
			uncovered = append(uncovered, comment)
		}
	}
	return uncovered, nil
}

func recordRequirementCommentReceipt(ctx context.Context, q *db.Queries, wakeup db.IssueWakeup, comment db.Comment) (db.IssueWakeupReceipt, error) {
	payload, _ := json.Marshal(map[string]any{
		"event_id": util.UUIDToString(comment.ID), "event_type": "comment.created", "version": 1,
		"occurred_at": comment.CreatedAt, "workspace_id": util.UUIDToString(wakeup.WorkspaceID),
		"issue_id": util.UUIDToString(wakeup.IssueID), "comment_id": util.UUIDToString(comment.ID),
		"actor_type": comment.AuthorType, "actor_id": util.UUIDToString(comment.AuthorID),
	})
	return q.RecordWakeupReceipt(ctx, db.RecordWakeupReceiptParams{
		ID: dbid.NewV7(), WakeupID: wakeup.ID, Revision: wakeup.Revision,
		EventKey: util.UUIDToString(comment.ID), EventType: "comment.created", Payload: payload,
	})
}

func createRequirementRecoveryTask(ctx context.Context, q *db.Queries, issue db.Issue, wakeup db.IssueWakeup, agent db.Agent, overlay runtimeMCPOverlayData, receipts []db.IssueWakeupReceipt, parentTaskID pgtype.UUID) (*db.AgentTaskQueue, []db.Comment, error) {
	uncovered, err := uncoveredRequirementComments(ctx, q, wakeup)
	if err != nil {
		return nil, nil, err
	}
	if len(uncovered) == 0 {
		return nil, nil, nil
	}
	latest := uncovered[len(uncovered)-1]
	coalesced := make([]pgtype.UUID, 0, len(uncovered)-1)
	for _, comment := range uncovered[:len(uncovered)-1] {
		coalesced = append(coalesced, comment.ID)
	}
	task, err := createWakeupTaskFromReceipts(ctx, q, createWakeupTaskParams{
		Issue: issue, Wakeup: wakeup, Receipts: receipts, Agent: agent, Overlay: overlay,
		TriggerCommentID: latest.ID, CoalescedCommentIDs: coalesced,
		RecoveryParentTaskID: parentTaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(receipts) > 0 {
		if err := q.ConsumeWakeupReceipts(ctx, db.ConsumeWakeupReceiptsParams{Ids: receiptIDs(receipts), TaskID: task.ID}); err != nil {
			return nil, nil, err
		}
	}
	if _, err := q.MarkRequirementIssueWakeupTriggered(ctx, db.MarkRequirementIssueWakeupTriggeredParams{
		ID: wakeup.ID, WorkspaceID: wakeup.WorkspaceID, ExpectedRevision: wakeup.Revision,
		TriggeredCommentID: latest.ID, LastTaskID: task.ID,
	}); err != nil {
		return nil, nil, err
	}
	if err := q.CountWakeupFires(ctx, wakeup.ID); err != nil {
		return nil, nil, err
	}
	return &task, uncovered, nil
}

func (s *IssueWakeupService) dispatchRequirementWakeup(ctx context.Context, q *db.Queries, issue db.Issue, wakeup db.IssueWakeup, agent db.Agent, overlay runtimeMCPOverlayData, note func(string, map[string]any) error) (*db.AgentTaskQueue, error) {
	if issue.Status != "blocked" {
		_, err := disableRequirementWakeupsForIssue(ctx, q, issue.WorkspaceID, issue.ID)
		return nil, err
	}
	requirement, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: wakeup.RequirementID, WorkspaceID: wakeup.WorkspaceID})
	if err != nil {
		return nil, err
	}
	available, err := requirementWakeupTargetAvailable(ctx, q, requirement, wakeup)
	if err != nil {
		return nil, err
	}
	if !available {
		_, err := disableRequirementWakeupsForIssue(ctx, q, issue.WorkspaceID, issue.ID)
		return nil, err
	}
	active, err := q.HasActiveRequirementRun(ctx, issue.ID)
	if err != nil || active || !wakeup.Enabled || wakeup.DisabledAt.Valid {
		return nil, err
	}
	receipts, err := q.ListPendingWakeupReceipts(ctx, db.ListPendingWakeupReceiptsParams{WakeupID: wakeup.ID, Revision: wakeup.Revision})
	if err != nil || len(receipts) == 0 {
		return nil, err
	}
	task, _, err := createRequirementRecoveryTask(ctx, q, issue, wakeup, agent, overlay, receipts, wakeup.SourceTaskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		if err := q.ConsumeWakeupReceipts(ctx, db.ConsumeWakeupReceiptsParams{Ids: receiptIDs(receipts)}); err != nil {
			return nil, err
		}
		if err := q.AdvanceIssueWakeup(ctx, db.AdvanceIssueWakeupParams{ID: wakeup.ID, Enabled: false}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := note(wakeupActivityTriggered, wakeupTriggerDetails(*task, receipts)); err != nil {
		return nil, err
	}
	return task, nil
}

type requirementRearmResult struct {
	Task       *db.AgentTaskQueue
	Activities []wakeupActivity
}

// rearmRequirementWakeup advances one stable wakeup row to its next revision in
// place. A completed run of an older revision only re-arms the question when
// the row still carries the revision that run was created from, the issue is
// still blocked, and no other run owns the issue.
func (s *IssueWakeupService) rearmRequirementWakeup(ctx context.Context, q *db.Queries, workspaceID, wakeupID pgtype.UUID, expectedRevision int64, parentTaskID pgtype.UUID) (requirementRearmResult, error) {
	var result requirementRearmResult
	stored, err := q.GetIssueWakeup(ctx, db.GetIssueWakeupParams{ID: wakeupID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !stored.RequirementID.Valid {
		return result, nil
	}
	row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: stored.RequirementID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if row.CancelledAt.Valid || !row.IssueID.Valid {
		return result, nil
	}
	issue, err := q.LockWakeupIssue(ctx, row.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if issue.Status != "blocked" {
		_, err := disableRequirementWakeupsForIssue(ctx, q, workspaceID, issue.ID)
		return result, err
	}
	wakeup, err := q.LockIssueWakeup(ctx, wakeupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if wakeup.DisabledAt.Valid || wakeup.Revision != expectedRevision || wakeup.IssueID != issue.ID || wakeup.RequirementID != stored.RequirementID {
		return result, nil
	}
	if wakeup.Enabled {
		return result, nil
	}
	active, err := q.HasActiveRequirementRun(ctx, issue.ID)
	if err != nil || active {
		return result, err
	}
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: wakeup.AgentID, WorkspaceID: workspaceID})
	if err != nil {
		return result, err
	}
	available, err := requirementWakeupTargetAvailable(ctx, q, row, wakeup)
	if err != nil {
		return result, err
	}
	if !available || s.authorize(ctx, q, workspaceID, wakeup.CreatedBy, agent) != nil {
		_, err := disableRequirementWakeupsForIssue(ctx, q, workspaceID, issue.ID)
		return result, err
	}
	source := wakeup.SourceTaskID
	if parentTaskID.Valid {
		source = parentTaskID
	}
	// Inputs captured for the previous revision are superseded by the rearm;
	// recovery records the comments this revision still has to cover.
	if err := q.DiscardWakeupReceipts(ctx, wakeup.ID); err != nil {
		return result, err
	}
	next, err := q.RearmRequirementIssueWakeup(ctx, db.RearmRequirementIssueWakeupParams{
		ID: wakeup.ID, WorkspaceID: workspaceID, ExpectedRevision: expectedRevision, SourceTaskID: source,
	})
	if err != nil {
		return result, err
	}
	comments, err := uncoveredRequirementComments(ctx, q, next)
	if err != nil {
		return result, err
	}
	if len(comments) == 0 {
		return result, nil
	}
	receipts := make([]db.IssueWakeupReceipt, 0, len(comments))
	for _, comment := range comments {
		receipt, err := recordRequirementCommentReceipt(ctx, q, next, comment)
		if err != nil {
			return result, err
		}
		receipts = append(receipts, receipt)
	}
	overlay := s.Tasks.buildRuntimeMCPOverlay(ctx, next.CreatedBy, agent)
	task, _, err := createRequirementRecoveryTask(ctx, q, issue, next, agent, overlay, receipts, parentTaskID)
	if err != nil {
		return result, err
	}
	result.Task = task
	if task != nil {
		triggered, err := recordWakeupActivity(ctx, q, next, wakeupActivityTriggered, "system", pgtype.UUID{}, wakeupTriggerDetails(*task, receipts))
		if err != nil {
			return result, err
		}
		result.Activities = append(result.Activities, triggered)
	}
	return result, nil
}

func (s *IssueWakeupService) finishRequirementRecovery(ctx context.Context, result requirementRearmResult) {
	s.publishWakeupActivities(result.Activities...)
	if result.Task != nil {
		s.Tasks.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *result.Task)
		s.Tasks.NotifyTaskEnqueued(ctx, *result.Task)
	}
}

func (s *IssueWakeupService) recoverRequirementWakeups(ctx context.Context, workspaceIDs []pgtype.UUID) error {
	candidates, err := s.Tasks.Queries.ListRequirementWakeupRecoveryCandidates(ctx, db.ListRequirementWakeupRecoveryCandidatesParams{WorkspaceIds: workspaceIDs, RowLimit: 100})
	if err != nil {
		return fmt.Errorf("list requirement wakeup recovery candidates: %w", err)
	}
	var errs []error
	for _, candidate := range candidates {
		var result requirementRearmResult
		err := func() error {
			tx, err := s.Tasks.TxStarter.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			result, err = s.rearmRequirementWakeup(ctx, s.Tasks.Queries.WithTx(tx), candidate.WorkspaceID, candidate.ID, candidate.Revision, pgtype.UUID{})
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		}()
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("rearm requirement wakeup %s: %w", util.UUIDToString(candidate.ID), err))
			continue
		}
		s.finishRequirementRecovery(ctx, result)
	}
	return errors.Join(errs...)
}
