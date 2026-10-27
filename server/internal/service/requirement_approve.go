package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const requirementApproveRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/approve"

type ApproveRequirementRequest struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	CandidateID      string `json:"candidateId"`
	CandidateDigest  string `json:"candidateDigest"`
}

type RequirementApproveResponseBuilder func(detail RequirementDetail, issue json.RawMessage) (json.RawMessage, error)

func (s *RequirementService) ApproveRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input ApproveRequirementRequest, idempotencyKey, requestID string, buildResponse RequirementApproveResponseBuilder) (RequirementActionResult, error) {
	if input.ExpectedRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_invalid", "expectedRevision must be positive")
	}
	if s.Issues == nil || s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement approve: issue and task services are required")
	}
	if buildResponse == nil {
		return RequirementActionResult{}, errors.New("requirement approve: response builder is required")
	}
	var prep IssueCreatePreparation
	var created IssueCreateResult
	var createParams IssueCreateParams
	var createOpts IssueCreateOpts
	hashInput := input
	hashInput.CandidateDigest = normalizeRequirementDigest(hashInput.CandidateDigest)
	result, err := s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "approve", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementApproveRoute,
		ExpectedRevision: &input.ExpectedRevision, Body: hashInput,
		Prepare: func(ctx context.Context) error {
			prep = s.Issues.prepareIssueCreate(ctx, IssueCreateParams{WorkspaceID: actor.WorkspaceID})
			return nil
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement approve: %w", err)
		}
		if row.Revision != input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, input.ExpectedRevision, row.Revision)
		}
		if row.CancelledAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is cancelled")
		}
		if row.IssueID.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementChatIssueExistsCode, "requirement already has an issue")
		}
		if !row.ChatSessionID.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_open_incomplete", "requirement chat must be opened before approval")
		}
		chat, err := q.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: row.ChatSessionID, WorkspaceID: actor.WorkspaceID})
		if err != nil || chat.AgentID != row.AgentID {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_open_incomplete", "requirement chat binding is unavailable")
		}
		if row.IntakeState != "awaiting_approval" {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_not_awaiting_approval", "requirement is not awaiting approval")
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || !roles["approver"] {
			return RequirementActionResponse{}, requirementForbidden()
		}
		agentRow, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: row.AgentID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "agent_not_found", "agent not found")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement approve agent: %w", err)
		}
		if agentRow.ArchivedAt.Valid || !CanMemberInvokeAgent(ctx, q, agentRow, actor.UserID, actor.WorkspaceID) {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "agent_invocation_not_allowed", "agent invocation is not allowed")
		}
		candidate, err := q.LockLatestRequirementCandidate(ctx, db.LockLatestRequirementCandidateParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_candidate_not_latest", "only the latest candidate can be approved")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock latest requirement candidate: %w", err)
		}
		if util.UUIDToString(candidate.ID) != input.CandidateID || normalizeRequirementDigest(input.CandidateDigest) != candidate.Digest {
			return RequirementActionResponse{}, requirementBadRequest("requirement_candidate_not_latest", "candidate id or digest does not match the latest candidate")
		}
		_, _, digest, err := CanonicalRequirementCandidateSnapshot(candidate.Snapshot, row)
		if err != nil || digest != candidate.Digest {
			return RequirementActionResponse{}, requirementBadRequest("requirement_candidate_invalid", "candidate snapshot or digest is invalid")
		}
		var snapshot RequirementCandidateSnapshot
		if err := json.Unmarshal(candidate.Snapshot, &snapshot); err != nil {
			return RequirementActionResponse{}, requirementBadRequest("requirement_candidate_invalid", "invalid candidate snapshot")
		}
		snapshotSource, channelSource, err := loadRequirementApproveSource(ctx, q, row)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		attachmentIDs, err := resolveRequirementCandidateAttachments(ctx, q, row, snapshot.AttachmentIDs, snapshotSource.Attachments)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		workspace, err := q.GetWorkspace(ctx, actor.WorkspaceID)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement approve workspace: %w", err)
		}
		createParams = IssueCreateParams{
			WorkspaceID: actor.WorkspaceID, Title: snapshot.Goal, Description: pgtype.Text{String: string(candidate.Snapshot), Valid: true},
			Status: "todo", Priority: "medium",
			AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: row.AgentID,
			CreatorType: "member", CreatorID: actor.UserID, ProjectID: row.ProjectID,
			AllowDuplicate: true, OriginType: pgtype.Text{String: "requirement_chat", Valid: true}, OriginID: row.ChatSessionID,
			AttachmentIDs: attachmentIDs,
		}
		createOpts = IssueCreateOpts{
			AssignedAgentRunFireAt: time.Now().Add(time.Minute), ChannelSource: channelSource,
			RequirementApproval: &RequirementApprovalContext{RequirementID: row.ID, ExpectedRevision: input.ExpectedRevision},
			ActorID:             util.UUIDToString(actor.UserID), AnalyticsAgentID: util.UUIDToString(row.AgentID), Platform: "requirement",
			BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
				return map[string]any{"issue": IssueToMapResolved(ctx, s.Queries, issue, workspace.IssuePrefix)}
			},
		}
		created, err = s.Issues.CreateInTx(ctx, tx, createParams, createOpts, prep)
		if errors.Is(err, ErrSourceAttachmentsUnavailable) {
			return RequirementActionResponse{}, requirementAttachmentUnavailable("")
		}
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !created.AssignedTaskID.Valid {
			return RequirementActionResponse{}, errors.New("requirement approve: initial task was not created")
		}
		bound, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: row.ID, WorkspaceID: actor.WorkspaceID})
		if err != nil || bound.IssueID != created.Issue.ID {
			return RequirementActionResponse{}, errors.New("requirement approve: issue binding was not persisted")
		}
		if channelSource != nil {
			linkedSource, err := q.GetChannelIssueSourceByIssue(ctx, created.Issue.ID)
			if err != nil || linkedSource.WorkspaceID != actor.WorkspaceID || linkedSource.InstallationID != channelSource.InstallationID ||
				linkedSource.ChannelType != channelSource.ChannelType || linkedSource.ChannelChatID != channelSource.ChatID ||
				linkedSource.ChatType != channelSource.ChatType || linkedSource.BindingID != channelSource.BindingID || linkedSource.RouteRevision != channelSource.RouteRevision {
				return RequirementActionResponse{}, requirementSourceLinkUnavailable("channel source association could not be verified")
			}
		}
		if err := createRequirementYixiezuoImport(ctx, q, actor, created.Issue, snapshotSource); err != nil {
			return RequirementActionResponse{}, err
		}
		approval, err := q.CreateRequirementApproval(ctx, db.CreateRequirementApprovalParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID, CandidateID: candidate.ID,
			CandidateDigest: candidate.Digest, ApprovedBy: actor.UserID, IssueID: created.Issue.ID, InitialTaskID: created.AssignedTaskID,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement approval: %w", err)
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", bound, "approved", row.IntakeState, "executing", map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "candidate_id": util.UUIDToString(candidate.ID),
			"approval_id": util.UUIDToString(approval.ID), "issue_id": util.UUIDToString(created.Issue.ID), "initial_task_id": util.UUIDToString(created.AssignedTaskID),
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutbox(ctx, q, bound, event, "requirement_status"); err != nil {
			return RequirementActionResponse{}, err
		}
		detail, err := s.BuildDetail(ctx, q, actor, bound, RequirementDetailOptions{ReadAuthorized: true})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		body, err := buildResponse(detail, detail.Issue)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement approve response: %w", err)
		}
		initialTask, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: created.AssignedTaskID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement initial task: %w", err)
		}
		if initialTask.RequirementID != row.ID || initialTask.Mode != "execute" || initialTask.IssueID != created.Issue.ID {
			return RequirementActionResponse{}, errors.New("requirement approve: initial task binding is invalid")
		}
		return RequirementActionResponse{
			RequirementID: row.ID, Status: http.StatusOK, Body: body,
			EventType: protocol.EventRequirementUpdated, Realtime: detail.RealtimePointer(), PromoteTasks: []db.AgentTaskQueue{initialTask},
		}, nil
	})
	if err != nil || result.Replay || !created.Issue.ID.Valid {
		return result, err
	}
	// The action transaction already committed the issue, the approval and the
	// deferred initial task, with the stored 200 response. FinalizeCreate only
	// performs post-commit side effects, so a failure here must not abort the
	// writer: otherwise PromoteTasks and requirement:updated are skipped, and a
	// replay of the same Idempotency-Key can never run them again.
	if _, finalizeErr := s.Issues.FinalizeCreate(ctx, created, createParams, createOpts); finalizeErr != nil {
		slog.Warn("requirement approve issue finalize failed after commit",
			"requirement_id", util.UUIDToString(requirementID),
			"issue_id", util.UUIDToString(created.Issue.ID),
			"error", finalizeErr)
	}
	return result, nil
}

func loadRequirementApproveSource(ctx context.Context, q *db.Queries, row db.Requirement) (yixiezuo.Snapshot, *ChannelIssueSource, error) {
	snapshot, payload, err := loadVerifiableRequirementSourcePreview(ctx, q, row)
	if err != nil {
		return yixiezuo.Snapshot{}, nil, err
	}
	if payload.Channel == nil {
		return snapshot, nil, nil
	}
	installationID, err := util.ParseUUID(payload.Channel.InstallationID)
	if err != nil {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source installation is invalid")
	}
	installation, err := q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID: installationID, WorkspaceID: row.WorkspaceID, ChannelType: "popo",
	})
	if err != nil || installation.Status != "active" || installation.AgentID != row.AgentID {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source installation is unavailable")
	}
	if payload.Draft != nil {
		return snapshot, &ChannelIssueSource{
			InstallationID: installation.ID, ChannelType: installation.ChannelType,
			ChatID: payload.Channel.ChatID, ChatType: payload.Channel.ChatType,
		}, nil
	}
	binding, err := q.LockCurrentChannelChatSessionBinding(ctx, db.LockCurrentChannelChatSessionBindingParams{
		InstallationID: installationID, ChannelChatID: payload.Channel.ChatID,
	})
	if err != nil || !binding.ID.Valid || binding.InstallationID != installationID || binding.ChannelChatID != payload.Channel.ChatID ||
		binding.ChannelType != "popo" || binding.ChatType != payload.Channel.ChatType {
		return yixiezuo.Snapshot{}, nil, requirementSourceLinkUnavailable("source chat binding is unavailable")
	}
	return snapshot, &ChannelIssueSource{
		InstallationID: installation.ID, ChannelType: installation.ChannelType,
		ChatID: binding.ChannelChatID, ChatType: binding.ChatType,
		BindingID: binding.ID, RouteRevision: binding.RouteRevision,
	}, nil
}

func createRequirementYixiezuoImport(ctx context.Context, q *db.Queries, actor RequirementActor, issue db.Issue, snapshot yixiezuo.Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal requirement source import: %w", err)
	}
	if _, err := q.CreateYixiezuoImport(ctx, db.CreateYixiezuoImportParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, SourceHost: snapshot.Source.Host,
		ExternalID: snapshot.Source.ID, SourceUrl: snapshot.Source.URL, Snapshot: encoded, ImportedBy: actor.UserID,
	}); err != nil {
		return requirementSourceLinkUnavailable("source import association could not be verified")
	}
	for key, value := range map[string]string{"yixiezuo_url": snapshot.Source.URL, "yixiezuo_id": snapshot.Source.ID, "yixiezuo_status": snapshot.Status} {
		raw, _ := json.Marshal(value)
		if _, err := q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key, Value: raw}); err != nil {
			return fmt.Errorf("persist requirement source metadata: %w", err)
		}
	}
	return nil
}
