package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	RequirementNotInReviewCode   = "requirement_not_in_review"
	RequirementIssueRequiredCode = "requirement_issue_required"
)

type AcceptRequirementRequest struct {
	ExpectedIssueRevision int64  `json:"expectedIssueRevision"`
	Summary               string `json:"summary,omitempty"`
	StatusName            string `json:"statusName,omitempty"`
}

type RejectRequirementRequest struct {
	ExpectedIssueRevision int64  `json:"expectedIssueRevision"`
	Reason                string `json:"reason"`
}

type CancelRequirementRequest struct {
	ExpectedRevision      *int64 `json:"expectedRevision,omitempty"`
	ExpectedIssueRevision *int64 `json:"expectedIssueRevision,omitempty"`
	Reason                string `json:"reason,omitempty"`
}

func requirementIssueRevisionConflict(id pgtype.UUID, expected, actual int64) *RequirementError {
	expectedCopy, actualCopy := expected, actual
	return &RequirementError{
		Status: http.StatusConflict, Code: "revision_conflict", Message: "resource changed since it was loaded",
		ResourceType: "issue", ResourceID: util.UUIDToString(id),
		ExpectedRevision: &expectedCopy, ActualRevision: &actualCopy,
	}
}

func (s *RequirementService) lockRequirementReviewAction(ctx context.Context, q *db.Queries, actor RequirementActor, requirementID pgtype.UUID, expectedIssueRevision int64) (db.Requirement, db.Issue, error) {
	row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Requirement{}, db.Issue{}, requirementNotFound()
	}
	if err != nil {
		return db.Requirement{}, db.Issue{}, fmt.Errorf("lock requirement review action: %w", err)
	}
	member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
	if err != nil {
		return db.Requirement{}, db.Issue{}, err
	}
	if !member || (!roles["requester"] && !roles["approver"]) {
		return db.Requirement{}, db.Issue{}, requirementForbidden()
	}
	if row.CancelledAt.Valid {
		return db.Requirement{}, db.Issue{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is cancelled")
	}
	if !row.IssueID.Valid {
		return db.Requirement{}, db.Issue{}, newRequirementError(http.StatusConflict, RequirementIssueRequiredCode, "requirement has no issue")
	}
	projection, err := s.requirementProjection(ctx, q, row, true)
	if err != nil {
		return db.Requirement{}, db.Issue{}, err
	}
	if projection.Issue == nil {
		return db.Requirement{}, db.Issue{}, newRequirementError(http.StatusConflict, RequirementIssueRequiredCode, "requirement issue is unavailable")
	}
	if projection.State != "in_review" {
		return db.Requirement{}, db.Issue{}, newRequirementError(http.StatusConflict, RequirementNotInReviewCode, "requirement is not in review")
	}
	issue := *projection.Issue
	if issue.Revision != expectedIssueRevision {
		return db.Requirement{}, db.Issue{}, requirementIssueRevisionConflict(issue.ID, expectedIssueRevision, issue.Revision)
	}
	return row, issue, nil
}

func requirementIssueUpdateParams(issue db.Issue, expectedRevision int64, status string) db.UpdateIssueParams {
	return db.UpdateIssueParams{
		ID: issue.ID, ExpectedRevision: pgtype.Int8{Int64: expectedRevision, Valid: true},
		Status:       pgtype.Text{String: status, Valid: true},
		AssigneeType: issue.AssigneeType, AssigneeID: issue.AssigneeID,
		StartDate: issue.StartDate, DueDate: issue.DueDate, ParentIssueID: issue.ParentIssueID,
		ProjectID: issue.ProjectID, Stage: issue.Stage,
	}
}

func stopRequirementIssueWakeups(ctx context.Context, q *db.Queries, issue db.Issue) ([]db.AgentTaskQueue, error) {
	cancelled, err := disableRequirementWakeupsForIssue(ctx, q, issue.WorkspaceID, issue.ID)
	if err != nil {
		return nil, err
	}
	closed, err := StopClosedIssueWakeups(ctx, q, issue)
	if err != nil {
		return nil, err
	}
	if err := SettleTerminalTaskState(ctx, q, closed...); err != nil {
		return nil, err
	}
	return append(cancelled, closed...), nil
}

func (s *RequirementService) requirementActionDetailResponse(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, response RequirementActionResponse) (RequirementActionResponse, error) {
	detail, err := s.BuildDetail(ctx, q, actor, row, RequirementDetailOptions{ReadAuthorized: true})
	if err != nil {
		return RequirementActionResponse{}, err
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return RequirementActionResponse{}, fmt.Errorf("marshal requirement detail: %w", err)
	}
	response.RequirementID = row.ID
	response.Status = http.StatusOK
	response.Body = body
	response.EventType = protocol.EventRequirementUpdated
	response.Realtime = detail.RealtimePointer()
	return response, nil
}

func requirementWritebackSource(ctx context.Context, q *db.Queries, row db.Requirement, issue db.Issue) (yixiezuo.Snapshot, *yixiezuo.ChannelScope, error) {
	link, err := q.GetYixiezuoImportByIssue(ctx, db.GetYixiezuoImportByIssueParams{WorkspaceID: row.WorkspaceID, IssueID: issue.ID})
	if err != nil {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source import is required for writeback")
	}
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal(link.Snapshot, &snapshot); err != nil ||
		snapshot.Source.Host != row.SourceHost || snapshot.Source.ID != row.ExternalID || snapshot.Source.URL != row.SourceUrl ||
		snapshot.LockVersion == nil || normalizeRequirementDigest(snapshot.Digest) != snapshot.Fingerprint() {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source snapshot is not valid for writeback")
	}
	operation, err := q.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{
		WorkspaceID: row.WorkspaceID, RequestedBy: row.RequesterID, ID: row.IntakeOperationID,
	})
	if err != nil || (operation.Kind != "preview" && operation.Kind != "draft") || operation.State != "succeeded" {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source preview is required for writeback")
	}
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(operation.Payload, &payload); err != nil || payload.Source != snapshot.Source {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source preview identity is invalid for writeback")
	}
	return snapshot, payload.Channel, nil
}
