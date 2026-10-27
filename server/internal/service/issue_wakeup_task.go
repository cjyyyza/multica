package service

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type createWakeupTaskParams struct {
	Issue                db.Issue
	Wakeup               db.IssueWakeup
	Previous             db.AgentTaskQueue
	Receipts             []db.IssueWakeupReceipt
	Chain                []string
	Agent                db.Agent
	Overlay              runtimeMCPOverlayData
	TriggerCommentID     pgtype.UUID
	CoalescedCommentIDs  []pgtype.UUID
	RecoveryParentTaskID pgtype.UUID
}

func createWakeupTaskFromReceipts(ctx context.Context, q *db.Queries, params createWakeupTaskParams) (db.AgentTaskQueue, error) {
	if err := guardIssueNotInTriage(ctx, q, params.Issue.ID, OriginNamed); err != nil {
		return db.AgentTaskQueue{}, err
	}
	w := params.Wakeup
	noteText, evidence := mergeWakeupEvidence(w, params.Previous, params.Receipts)
	contextValues := map[string]any{
		"wakeup_id":       util.UUIDToString(w.ID),
		"wakeup_revision": w.Revision,
		"wakeup_evidence": evidence,
		"wakeup_chain":    params.Chain,
	}
	if w.RequirementID.Valid {
		contextValues["requirement_question_comment_id"] = util.UUIDToString(w.ParentCommentID)
		contextValues["requirement_wakeup_generation"] = w.Revision
		if params.RecoveryParentTaskID.Valid {
			contextValues["requirement_recovery_parent_task_id"] = util.UUIDToString(params.RecoveryParentTaskID)
		}
	}
	contextJSON, _ := json.Marshal(contextValues)
	triggerCommentID := params.TriggerCommentID
	if !triggerCommentID.Valid {
		triggerCommentID = w.ParentCommentID
	}
	delegatedFromTaskID := w.SourceTaskID
	if params.RecoveryParentTaskID.Valid {
		delegatedFromTaskID = params.RecoveryParentTaskID
	}
	return q.CreateWakeupTask(ctx, db.CreateWakeupTaskParams{
		ID: dbid.NewV7(), AgentID: w.AgentID, RuntimeID: params.Agent.RuntimeID, IssueID: w.IssueID,
		Priority: priorityToInt(params.Issue.Priority), TriggerCommentID: triggerCommentID, CoalescedCommentIds: params.CoalescedCommentIDs,
		TriggerSummary: pgtype.Text{String: "Wakeup: " + truncateForSummary(w.Instruction, 160), Valid: true},
		HandoffNote:    pgtype.Text{String: noteText, Valid: true}, OriginatorUserID: w.CreatedBy, AccountableUserID: w.CreatedBy,
		OriginatorSource: pgtype.Text{String: "trigger_owner", Valid: true}, TriggerEvidenceKind: pgtype.Text{String: "issue_wakeup", Valid: true},
		TriggerEvidenceRefID: w.ID, DelegatedFromTaskID: delegatedFromTaskID, WakeupContext: contextJSON,
		RuntimeMcpOverlay: params.Overlay.Overlay, RuntimeConnectedApps: params.Overlay.ConnectedApps,
	})
}
