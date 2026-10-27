package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type validatedRequirementSource struct {
	Source      yixiezuo.Source
	Snapshot    json.RawMessage
	Digest      string
	LockVersion pgtype.Int8
}

func validateCreateRequirementSource(input CreateRequirementRequest) (validatedRequirementSource, error) {
	if strings.TrimSpace(input.SourceHost) == "" || strings.TrimSpace(input.ExternalID) == "" || strings.TrimSpace(input.SourceDigest) == "" {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "sourceHost, externalId and sourceDigest are required")
	}
	source, err := yixiezuo.ParseSource(strings.TrimSpace(input.SourceUrl))
	if err != nil {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", err.Error())
	}
	host := strings.ToLower(strings.TrimSpace(input.SourceHost))
	if host != "yixiezuo" && host != source.Host {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "sourceHost does not match the source URL")
	}
	if strings.TrimSpace(input.ExternalID) != source.ID {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "externalId does not match the source URL")
	}
	if trimmed := bytes.TrimSpace(input.SourceSnapshot); len(trimmed) == 0 || trimmed[0] != '{' {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "sourceSnapshot must be a JSON object")
	}
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal(input.SourceSnapshot, &snapshot); err != nil {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "invalid source snapshot")
	}
	if err := snapshot.Validate(source); err != nil {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", err.Error())
	}
	digest := snapshot.Fingerprint()
	if normalizeRequirementDigest(input.SourceDigest) != digest || normalizeRequirementDigest(snapshot.Digest) != digest {
		return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "sourceDigest does not match the canonical source snapshot")
	}
	lockVersion := pgtype.Int8{}
	if snapshot.LockVersion != nil {
		if *snapshot.LockVersion < 0 {
			return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "source lock version must be non-negative")
		}
		lockVersion = pgtype.Int8{Int64: *snapshot.LockVersion, Valid: true}
	}
	if input.SourceLockVersion != nil {
		if !lockVersion.Valid || *input.SourceLockVersion != lockVersion.Int64 {
			return validatedRequirementSource{}, requirementBadRequest("requirement_source_invalid", "sourceLockVersion does not match the source snapshot")
		}
	}
	return validatedRequirementSource{
		Source: source, Snapshot: append(json.RawMessage(nil), input.SourceSnapshot...),
		Digest: digest, LockVersion: lockVersion,
	}, nil
}

func validateStoredRequirementSource(row db.Requirement) bool {
	var lockVersion *int64
	if row.SourceLockVersion.Valid {
		value := row.SourceLockVersion.Int64
		lockVersion = &value
	}
	validated, err := validateCreateRequirementSource(CreateRequirementRequest{
		SourceHost: row.SourceHost, ExternalID: row.ExternalID, SourceUrl: row.SourceUrl,
		SourceSnapshot: row.SourceSnapshot, SourceDigest: row.SourceDigest, SourceLockVersion: lockVersion,
	})
	return err == nil && validated.Source.Host == row.SourceHost && validated.Source.ID == row.ExternalID &&
		validated.Source.URL == row.SourceUrl && validated.Digest == row.SourceDigest
}

func normalizeRequirementDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}

func decodeRequirementJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func (s *RequirementService) validateRequirementIntake(ctx context.Context, q *db.Queries, actor RequirementActor, input CreateRequirementRequest, source validatedRequirementSource) (pgtype.UUID, error) {
	if input.IntakeOperationID == nil || strings.TrimSpace(*input.IntakeOperationID) == "" {
		return pgtype.UUID{}, nil
	}
	operationID, err := util.ParseUUID(strings.TrimSpace(*input.IntakeOperationID))
	if err != nil {
		return pgtype.UUID{}, requirementBadRequest("requirement_intake_invalid", "invalid intakeOperationId")
	}
	row, err := q.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{
		WorkspaceID: actor.WorkspaceID, RequestedBy: actor.UserID, ID: operationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, newRequirementError(http.StatusForbidden, "requirement_source_forbidden", "intake operation is not readable by this actor")
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("load intake operation: %w", err)
	}
	if (row.Kind != "preview" && row.Kind != "draft") || row.State != "succeeded" {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_intake_not_ready", "intake operation is not a succeeded source preview")
	}
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil || payload.Source != source.Source {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_source_mismatch", "intake operation belongs to a different source")
	}
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal(row.Result, &snapshot); err != nil || snapshot.Validate(source.Source) != nil {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_source_mismatch", "intake operation has no valid source snapshot")
	}
	if row.Kind == "draft" && (payload.Draft == nil || payload.Draft.AgentID != strings.ToLower(input.AgentID) || yixiezuo.ValidateDraftSnapshot(util.UUIDToString(row.ID), *payload.Draft, snapshot) != nil) {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_source_mismatch", "draft intent does not match this requirement")
	}
	if snapshot.Fingerprint() != source.Digest || normalizeRequirementDigest(snapshot.Digest) != source.Digest {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_source_mismatch", "intake operation snapshot does not match the request")
	}
	if (snapshot.LockVersion == nil) != (!source.LockVersion.Valid) ||
		(snapshot.LockVersion != nil && *snapshot.LockVersion != source.LockVersion.Int64) {
		return pgtype.UUID{}, newRequirementError(http.StatusConflict, "requirement_source_mismatch", "intake operation lock version does not match the request")
	}
	return operationID, nil
}

// ValidateDraftAgent rejects an unavailable Plan route before any source creation is queued.
func (s *RequirementService) ValidateDraftAgent(ctx context.Context, actor RequirementActor, agentID pgtype.UUID) error {
	if err := s.requireFlow(ctx); err != nil {
		return err
	}
	if err := requireRequirementMember(ctx, s.Queries, actor); err != nil {
		return err
	}
	agentRow, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return newRequirementError(http.StatusNotFound, "agent_not_found", "agent not found")
	}
	if err != nil {
		return err
	}
	if agentRow.ArchivedAt.Valid || !CanMemberInvokeAgent(ctx, s.Queries, agentRow, actor.UserID, actor.WorkspaceID) {
		return requirementForbidden()
	}
	return validateRequirementPlanRuntime(ctx, s.Queries, agentRow)
}
