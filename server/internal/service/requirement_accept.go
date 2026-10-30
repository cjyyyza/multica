package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const requirementAcceptRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/accept"

func (s *RequirementService) AcceptRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input AcceptRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.ExpectedIssueRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_issue_revision_invalid", "expectedIssueRevision must be positive")
	}
	input.Summary = strings.TrimSpace(input.Summary)
	input.StatusName = strings.TrimSpace(input.StatusName)
	if len(input.Summary) > 20000 {
		return RequirementActionResult{}, requirementBadRequest("requirement_summary_invalid", "result summary must not exceed 20000 bytes")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement accept: task service is required")
	}
	actionInput := RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "accept", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementAcceptRoute,
		ExpectedRevision: &input.ExpectedIssueRevision, Body: input,
	}
	requestHash, err := RequirementActionHash(actionInput)
	if err != nil {
		return RequirementActionResult{}, err
	}
	actionInput.RequestHash = requestHash
	return s.ExecuteAction(ctx, actionInput, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, issue, err := s.lockRequirementReviewAction(ctx, q, actor, requirementID, input.ExpectedIssueRevision)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		acceptParams := requirementIssueUpdateParams(issue, input.ExpectedIssueRevision, "done")
		acceptParams.RequirementAction = RequirementIssueAction(RequirementIssueActionAccept)
		updatedIssue, err := UpdateIssueWithRequirementGate(ctx, q, acceptParams)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		cancelledWakeups, err := stopRequirementIssueWakeups(ctx, q, updatedIssue)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("stop accepted requirement wakeups: %w", err)
		}
		updatedRequirement, err := q.AdvanceRequirementRevision(ctx, db.AdvanceRequirementRevisionParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: row.Revision,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("advance accepted requirement revision: %w", err)
		}
		_, native, err := requirementDelegation(updatedRequirement)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		var operationID pgtype.UUID
		if native {
			if input.StatusName != "" {
				return RequirementActionResponse{}, requirementBadRequest("requirement_source_status_invalid", "native delegations do not have a source workflow")
			}
		} else {
			operation, err := s.createRequirementAcceptWriteback(ctx, q, actor, updatedRequirement, updatedIssue, input, idempotencyKey, requestHash)
			if err != nil {
				return RequirementActionResponse{}, err
			}
			operationID = operation.ID
		}
		refs := map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "issue_id": util.UUIDToString(issue.ID),
			"issue_revision": updatedIssue.Revision,
		}
		if operationID.Valid {
			refs["operation_id"] = util.UUIDToString(operationID)
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", updatedRequirement, "accepted", "in_review", "done", refs, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutboxWithOptions(ctx, q, updatedRequirement, event, "requirement_status", RequirementOutboxOptions{
			OperationID:   operationID,
			IssueRevision: pgtype.Int8{Int64: updatedIssue.Revision, Valid: true},
		}); err != nil {
			return RequirementActionResponse{}, err
		}
		return s.requirementActionDetailResponse(ctx, q, actor, updatedRequirement, RequirementActionResponse{
			IssueUpdates:   []RequirementIssueUpdate{{Issue: updatedIssue, PreviousStatus: issue.Status}},
			CancelledTasks: cancelledWakeups,
		})
	})
}

func (s *RequirementService) createRequirementAcceptWriteback(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, issue db.Issue, input AcceptRequirementRequest, idempotencyKey, requestHash string) (db.YixiezuoOperation, error) {
	snapshot, channel, err := requirementWritebackSource(ctx, q, row, issue)
	if err != nil {
		return db.YixiezuoOperation{}, err
	}
	if input.StatusName != "" {
		found := false
		for _, status := range snapshot.Statuses {
			if status.Name == input.StatusName {
				found = true
				break
			}
		}
		if !found {
			return db.YixiezuoOperation{}, requirementBadRequest("requirement_source_status_invalid", "select a status from the source workflow")
		}
	}
	summary := input.Summary
	if summary == "" {
		summary = "Requirement accepted in Multica"
	}
	summary = "Multica status: done\n\n" + summary
	issueURL := ""
	if s.AppURL != "" {
		workspace, err := q.GetWorkspace(ctx, row.WorkspaceID)
		if err != nil {
			return db.YixiezuoOperation{}, fmt.Errorf("load accepted requirement workspace: %w", err)
		}
		issueURL = strings.TrimRight(s.AppURL, "/") + "/" + url.PathEscape(workspace.Slug) + "/issues/" + util.UUIDToString(issue.ID)
		summary += "\n\n[Multica Issue](" + issueURL + ")"
	}
	payload, err := json.Marshal(yixiezuo.OperationPayload{
		Source: snapshot.Source, ExpectedDigest: snapshot.Digest, Revision: issue.Revision,
		Summary: summary, StatusName: input.StatusName, IssueURL: issueURL, Channel: channel,
	})
	if err != nil {
		return db.YixiezuoOperation{}, fmt.Errorf("marshal requirement accept writeback: %w", err)
	}
	gateError := ""
	if !featureflags.PMMCPWriteEnabled(ctx, s.FeatureFlags) {
		gateError = "pmmcp_write is disabled; requirement publication remains pending"
	}
	operation, err := q.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{
		WorkspaceID: row.WorkspaceID, RequestedBy: actor.UserID, Kind: "publish", IssueID: issue.ID,
		Payload:       payload,
		RequestKey:    pgtype.Text{String: "requirement-accept:" + strings.TrimSpace(idempotencyKey), Valid: true},
		RequirementID: row.ID,
		RequestHash:   pgtype.Text{String: requestHash, Valid: requestHash != ""},
		Error:         gateError,
	})
	if err != nil {
		return db.YixiezuoOperation{}, fmt.Errorf("create requirement accept writeback: %w", err)
	}
	return operation, nil
}
