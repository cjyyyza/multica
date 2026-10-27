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
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	requirementCreateRoute       = "POST /api/workspaces/{id}/requirements"
	requirementParticipantsRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/participants"
)

type createRequirementHashBody struct {
	SourceHost        string                             `json:"sourceHost"`
	ExternalID        string                             `json:"externalId"`
	SourceUrl         string                             `json:"sourceUrl"`
	SourceSnapshot    any                                `json:"sourceSnapshot"`
	SourceDigest      string                             `json:"sourceDigest"`
	SourceLockVersion *int64                             `json:"sourceLockVersion"`
	AgentID           string                             `json:"agentId"`
	ProjectID         *string                            `json:"projectId"`
	Participants      []normalizedRequirementParticipant `json:"participants"`
	IntakeOperationID *string                            `json:"intakeOperationId"`
}

func (s *RequirementService) CreateRequirement(ctx context.Context, actor RequirementActor, input CreateRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	agentID, err := util.ParseUUID(input.AgentID)
	if err != nil {
		return RequirementActionResult{}, requirementBadRequest("requirement_agent_invalid", "agentId must be a UUID")
	}
	projectID, err := optionalRequirementUUID(input.ProjectID, "projectId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	intakeOperationID, err := optionalRequirementUUID(input.IntakeOperationID, "intakeOperationId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	participants, err := createRequirementParticipants(input.Participants, actor.UserID)
	if err != nil {
		return RequirementActionResult{}, err
	}
	source, err := validateCreateRequirementSource(input)
	if err != nil {
		return RequirementActionResult{}, err
	}
	snapshotValue, err := decodeRequirementJSONValue(input.SourceSnapshot)
	if err != nil {
		return RequirementActionResult{}, requirementBadRequest("requirement_source_invalid", "sourceSnapshot must be a JSON object")
	}
	hashBody := createRequirementHashBody{
		SourceHost: strings.ToLower(strings.TrimSpace(input.SourceHost)), ExternalID: strings.TrimSpace(input.ExternalID),
		SourceUrl: strings.TrimSpace(input.SourceUrl), SourceSnapshot: snapshotValue, SourceDigest: normalizeRequirementDigest(input.SourceDigest),
		SourceLockVersion: input.SourceLockVersion, AgentID: util.UUIDToString(agentID), ProjectID: intakeNilString(projectID),
		Participants: participants, IntakeOperationID: intakeNilString(intakeOperationID),
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, ActorType: "member", ActorID: actor.UserID,
		Action: "create", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementCreateRoute, Body: hashBody,
	}, func(ctx context.Context, _ pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := requireRequirementMember(ctx, q, actor); err != nil {
			return RequirementActionResponse{}, err
		}
		agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "agent_not_found", "agent not found")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement agent: %w", err)
		}
		if agent.ArchivedAt.Valid {
			return RequirementActionResponse{}, requirementBadRequest("agent_not_available", "agent is archived")
		}
		if !CanMemberInvokeAgent(ctx, q, agent, actor.UserID, actor.WorkspaceID) {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "agent_invocation_not_allowed", "agent invocation is not allowed")
		}
		if err := validateRequirementPlanRuntime(ctx, q, agent); err != nil {
			return RequirementActionResponse{}, err
		}
		if projectID.Valid {
			if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: actor.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
				return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "project_not_found", "project not found")
			} else if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("load requirement project: %w", err)
			}
		}
		validatedIntakeID, err := s.validateRequirementIntake(ctx, q, actor, input, source)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := validateRequirementParticipantMembers(ctx, q, actor.WorkspaceID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		row, err := q.CreateRequirement(ctx, db.CreateRequirementParams{
			WorkspaceID: actor.WorkspaceID, SourceHost: source.Source.Host, ExternalID: source.Source.ID,
			SourceUrl: source.Source.URL, SourceSnapshot: source.Snapshot, SourceDigest: source.Digest,
			SourceLockVersion: source.LockVersion, RequesterID: actor.UserID, AgentID: agentID, ProjectID: projectID,
			IntakeOperationID: validatedIntakeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			existing, loadErr := q.GetRequirementBySource(ctx, db.GetRequirementBySourceParams{
				WorkspaceID: actor.WorkspaceID, SourceHost: source.Source.Host, ExternalID: source.Source.ID,
			})
			if loadErr != nil {
				return RequirementActionResponse{}, fmt.Errorf("load existing requirement source: %w", loadErr)
			}
			allowed, accessErr := s.requirementReadAuthorized(ctx, q, actor, existing)
			if accessErr != nil {
				return RequirementActionResponse{}, accessErr
			}
			conflict := newRequirementError(http.StatusConflict, "requirement_source_already_exists", "a requirement already exists for this source")
			if allowed {
				conflict.ExistingRequirementID = util.UUIDToString(existing.ID)
			}
			return RequirementActionResponse{}, conflict
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement: %w", err)
		}
		if err := insertRequirementParticipants(ctx, q, actor.WorkspaceID, row.ID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", row, "created", "", row.IntakeState, map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "agent_id": util.UUIDToString(row.AgentID), "project_id": requirementUUIDPtr(row.ProjectID),
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		kind := "requirement_status"
		if row.IntakeOperationID.Valid {
			kind = "requirement_draft"
		}
		if err := s.appendRequirementOutbox(ctx, q, row, event, kind); err != nil {
			return RequirementActionResponse{}, err
		}
		detail, err := s.BuildDetail(ctx, q, actor, row, RequirementDetailOptions{ReadAuthorized: true})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		body, err := json.Marshal(detail)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement detail: %w", err)
		}
		return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusCreated, Body: body, EventType: "requirement:created", Realtime: detail.RealtimePointer()}, nil
	})
}

func optionalRequirementUUID(raw *string, field string) (pgtype.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.UUID{}, nil
	}
	id, err := util.ParseUUID(strings.TrimSpace(*raw))
	if err != nil {
		return pgtype.UUID{}, requirementBadRequest("requirement_id_invalid", field+" must be a UUID")
	}
	return id, nil
}

func intakeNilString(value pgtype.UUID) *string {
	return requirementUUIDPtr(value)
}

func requireRequirementMember(ctx context.Context, q *db.Queries, actor RequirementActor) error {
	_, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: actor.UserID, WorkspaceID: actor.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return requirementForbidden()
	}
	if err != nil {
		return fmt.Errorf("load requirement actor member: %w", err)
	}
	return nil
}

func (s *RequirementService) GetRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID) (RequirementDetail, error) {
	row, err := s.Queries.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementDetail{}, requirementNotFound()
	}
	if err != nil {
		return RequirementDetail{}, fmt.Errorf("load requirement: %w", err)
	}
	return s.requirementDetailForActor(ctx, s.Queries, actor, row)
}

func (s *RequirementService) GetRequirementForTask(ctx context.Context, actor RequirementActor, taskID, requirementID pgtype.UUID, mode string) (RequirementDetail, error) {
	if mode != "plan" && mode != "execute" {
		return RequirementDetail{}, requirementForbidden()
	}
	if mode == "plan" {
		if s.Tasks == nil {
			return RequirementDetail{}, errors.New("requirement plan read: task service is required")
		}
		task, err := s.Tasks.ValidateTaskForRequirementCandidate(ctx, taskID, requirementID, actor.WorkspaceID)
		if err != nil {
			return RequirementDetail{}, requirementCandidateTaskError(err)
		}
		if task.AgentID != actor.UserID {
			return RequirementDetail{}, requirementForbidden()
		}
	}
	row, err := s.Queries.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementDetail{}, requirementNotFound()
	}
	if err != nil {
		return RequirementDetail{}, fmt.Errorf("load task requirement: %w", err)
	}
	if mode == "execute" {
		if !row.IssueID.Valid || row.CancelledAt.Valid {
			return RequirementDetail{}, requirementForbidden()
		}
		if err := validateRequirementExecuteTask(ctx, s.Queries, actor, row, taskID); err != nil {
			return RequirementDetail{}, err
		}
	}
	return s.BuildDetail(ctx, s.Queries, actor, row, RequirementDetailOptions{ReadAuthorized: true, CandidateAuthorized: mode == "plan"})
}

func (s *RequirementService) GetRequirementByChat(ctx context.Context, actor RequirementActor, chatSessionID pgtype.UUID) (RequirementDetail, error) {
	row, err := s.Queries.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
		WorkspaceID: actor.WorkspaceID, ChatSessionID: chatSessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementDetail{}, requirementNotFound()
	}
	if err != nil {
		return RequirementDetail{}, fmt.Errorf("load requirement by chat: %w", err)
	}
	return s.requirementDetailForActor(ctx, s.Queries, actor, row)
}

func (s *RequirementService) requirementDetailForActor(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement) (RequirementDetail, error) {
	detail, err := s.BuildDetail(ctx, q, actor, row, RequirementDetailOptions{})
	if err != nil {
		return RequirementDetail{}, err
	}
	if !detail.Permissions.CanRead {
		return RequirementDetail{}, requirementForbidden()
	}
	return detail, nil
}

type updateParticipantsHashBody struct {
	ExpectedRevision int64                              `json:"expectedRevision"`
	Participants     []normalizedRequirementParticipant `json:"participants"`
}

func (s *RequirementService) UpdateRequirementParticipants(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input UpdateRequirementParticipantsRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.ExpectedRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_invalid", "expectedRevision must be positive")
	}
	normalized, err := normalizeRequirementParticipants(input.Participants)
	if err != nil {
		return RequirementActionResult{}, err
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "participants", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementParticipantsRoute,
		ExpectedRevision: &input.ExpectedRevision,
		Body:             updateParticipantsHashBody{ExpectedRevision: input.ExpectedRevision, Participants: normalized},
	}, func(ctx context.Context, _ pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement: %w", err)
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || !roles["requester"] {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if row.Revision != input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, input.ExpectedRevision, row.Revision)
		}
		projection, err := s.requirementProjection(ctx, q, row, true)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if requirementTerminalState(projection.State) {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_terminal", "participants cannot be changed in a terminal state")
		}
		participants, err := updateRequirementParticipants(input.Participants, row.RequesterID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := validateRequirementParticipantMembers(ctx, q, actor.WorkspaceID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := requireRequirementBlockedTargetsRetained(ctx, q, row, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		updated, err := q.AdvanceRequirementRevision(ctx, db.AdvanceRequirementRevisionParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: input.ExpectedRevision,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("advance requirement revision: %w", err)
		}
		if err := q.DeleteRequirementParticipants(ctx, db.DeleteRequirementParticipantsParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID,
		}); err != nil {
			return RequirementActionResponse{}, fmt.Errorf("replace requirement participants: %w", err)
		}
		if err := insertRequirementParticipants(ctx, q, actor.WorkspaceID, row.ID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := fenceRemovedRequirementRecipients(ctx, q, actor.WorkspaceID, row.ID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", updated, "participants_updated", "", "", map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "revision": updated.Revision,
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutbox(ctx, q, updated, event, "requirement_status"); err != nil {
			return RequirementActionResponse{}, err
		}
		detail, err := s.BuildDetail(ctx, q, actor, updated, RequirementDetailOptions{ReadAuthorized: true})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		body, err := json.Marshal(detail)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement detail: %w", err)
		}
		return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusOK, Body: body, EventType: "requirement:updated", Realtime: detail.RealtimePointer()}, nil
	})
}

func appendRequirementEvent(ctx context.Context, q *db.Queries, actor RequirementActor, actorType string, row db.Requirement, action, fromState, toState string, refs map[string]any, requestID string) (db.RequirementEvent, error) {
	entityRefs, err := json.Marshal(refs)
	if err != nil {
		return db.RequirementEvent{}, fmt.Errorf("marshal requirement event refs: %w", err)
	}
	event, err := q.AppendRequirementEvent(ctx, db.AppendRequirementEventParams{
		WorkspaceID: actor.WorkspaceID, RequirementID: row.ID, ActorType: actorType,
		ActorID: actor.UserID, Action: action,
		FromState:  pgtype.Text{String: fromState, Valid: fromState != ""},
		ToState:    pgtype.Text{String: toState, Valid: toState != ""},
		EntityRefs: entityRefs, RequestID: pgtype.Text{String: requestID, Valid: requestID != ""},
	})
	if err != nil {
		return db.RequirementEvent{}, fmt.Errorf("append requirement event: %w", err)
	}
	return event, nil
}
