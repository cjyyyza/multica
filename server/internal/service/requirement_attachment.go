package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const RequirementAttachmentUnavailableCode = "requirement_attachment_unavailable"

func requirementAttachmentUnavailable(externalID string) *RequirementError {
	message := "selected requirement attachment is not present in the verifiable source preview or has no available local mirror"
	if externalID != "" {
		message = "requirement attachment " + externalID + " is not present in the verifiable source preview or has no available local mirror"
	}
	return newRequirementError(http.StatusConflict, RequirementAttachmentUnavailableCode, message)
}

func loadVerifiableRequirementSourcePreview(ctx context.Context, q *db.Queries, row db.Requirement) (yixiezuo.Snapshot, yixiezuo.OperationPayload, error) {
	if !row.IntakeOperationID.Valid {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("a verifiable source operation is required before approval")
	}
	operation, err := q.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{
		WorkspaceID: row.WorkspaceID, RequestedBy: row.RequesterID, ID: row.IntakeOperationID,
	})
	if err != nil || (operation.Kind != "preview" && operation.Kind != "draft") || operation.State != "succeeded" {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("a succeeded source preview is required before approval")
	}
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source preview identity is invalid")
	}
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal(operation.Result, &snapshot); err != nil || snapshot.Source != payload.Source {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source preview identity is invalid")
	}
	if snapshot.Source.Host != row.SourceHost || snapshot.Source.ID != row.ExternalID || snapshot.Source.URL != row.SourceUrl ||
		snapshot.Fingerprint() != row.SourceDigest || normalizeRequirementDigest(snapshot.Digest) != row.SourceDigest {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source preview does not match the requirement")
	}
	if err := snapshot.Validate(payload.Source); err != nil {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source snapshot is invalid")
	}
	if operation.Kind == "draft" && (payload.Draft == nil || payload.Draft.AgentID != util.UUIDToString(row.AgentID) || yixiezuo.ValidateDraftSnapshot(util.UUIDToString(operation.ID), *payload.Draft, snapshot) != nil) {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source draft intent is invalid")
	}
	if (snapshot.LockVersion == nil) != (!row.SourceLockVersion.Valid) || (snapshot.LockVersion != nil && *snapshot.LockVersion != row.SourceLockVersion.Int64) {
		return yixiezuo.Snapshot{}, yixiezuo.OperationPayload{}, requirementSourceLinkUnavailable("source lock version does not match")
	}
	return snapshot, payload, nil
}

func resolveRequirementCandidateAttachments(ctx context.Context, q *db.Queries, row db.Requirement, selectedIDs []string, sourceAttachments []yixiezuo.Attachment) ([]pgtype.UUID, error) {
	if len(selectedIDs) == 0 {
		return nil, nil
	}
	byExternalID := make(map[string]yixiezuo.Attachment, len(sourceAttachments))
	duplicates := make(map[string]bool, len(sourceAttachments))
	for _, attachment := range sourceAttachments {
		if _, exists := byExternalID[attachment.ID]; exists {
			duplicates[attachment.ID] = true
			continue
		}
		byExternalID[attachment.ID] = attachment
	}
	resolved := make([]pgtype.UUID, 0, len(selectedIDs))
	for _, externalID := range selectedIDs {
		source, ok := byExternalID[externalID]
		if !ok || duplicates[externalID] || strings.TrimSpace(source.LocalID) == "" {
			return nil, requirementAttachmentUnavailable(externalID)
		}
		localID, err := util.ParseUUID(strings.TrimSpace(source.LocalID))
		if err != nil {
			return nil, requirementAttachmentUnavailable(externalID)
		}
		attachment, err := q.GetAttachment(ctx, db.GetAttachmentParams{ID: localID, WorkspaceID: row.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, requirementAttachmentUnavailable(externalID)
		}
		if err != nil {
			return nil, fmt.Errorf("load requirement attachment mirror: %w", err)
		}
		if attachment.SourceContextID.Valid || attachment.ChatSessionID.Valid || attachment.ChatMessageID.Valid {
			return nil, requirementAttachmentUnavailable(externalID)
		}
		resolved = append(resolved, localID)
	}
	return resolved, nil
}
