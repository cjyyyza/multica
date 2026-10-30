package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	requirementDelegationRoute      = "POST /api/workspaces/{id}/requirement-delegations"
	requirementDelegationSourceHost = "multica"
	requirementDelegationSourceKind = "squad_delegation"
	requirementSquadRequestKind     = "squad_request"
	requirementAgentRequestKind     = "agent_request"
)

type RequirementDelegationCandidateInput struct {
	Goal               string   `json:"goal"`
	NonGoals           []string `json:"nonGoals"`
	Scope              []string `json:"scope"`
	Constraints        []string `json:"constraints"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	ProjectID          *string  `json:"projectId"`
}

type CreateRequirementDelegationRequest struct {
	ExternalRef  string                              `json:"externalRef"`
	SquadID      string                              `json:"squadId"`
	Candidate    RequirementDelegationCandidateInput `json:"candidate"`
	Participants []RequirementParticipantInput       `json:"participants"`
}

type requirementDelegationSource struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	SquadID     string `json:"squadId,omitempty"`
	ExternalRef string `json:"externalRef"`
	Idea        string `json:"idea,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
}

func (source requirementDelegationSource) hasPlan() bool {
	return source.Kind == requirementSquadRequestKind || source.Kind == requirementAgentRequestKind
}

type createRequirementDelegationHashBody struct {
	ExternalRef  string                              `json:"externalRef"`
	SquadID      string                              `json:"squadId"`
	Candidate    RequirementDelegationCandidateInput `json:"candidate"`
	Participants []normalizedRequirementParticipant  `json:"participants"`
}

func requirementDelegation(row db.Requirement) (requirementDelegationSource, bool, error) {
	if row.SourceHost != requirementDelegationSourceHost {
		return requirementDelegationSource{}, false, nil
	}
	var source requirementDelegationSource
	decoder := json.NewDecoder(bytes.NewReader(row.SourceSnapshot))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement source is invalid")
	}
	if source.Kind == requirementDelegationSourceKind || source.Kind == requirementSquadRequestKind {
		squadID, err := util.ParseUUID(source.SquadID)
		if err != nil || source.AgentID != "" {
			return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement squad is invalid")
		}
		source.SquadID = util.UUIDToString(squadID)
	} else if source.Kind == requirementAgentRequestKind {
		agentID, err := util.ParseUUID(source.AgentID)
		if err != nil || agentID != row.AgentID || source.SquadID != "" {
			return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement agent is invalid")
		}
		source.AgentID = util.UUIDToString(agentID)
	} else {
		return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement kind is invalid")
	}
	source.ExternalRef = strings.TrimSpace(source.ExternalRef)
	source.Title = strings.TrimSpace(source.Title)
	source.Idea = strings.TrimSpace(source.Idea)
	if (source.hasPlan() && source.Idea == "") || (!source.hasPlan() && source.Idea != "") || source.ExternalRef == "" || source.ExternalRef != row.ExternalID ||
		row.SourceUrl != "" || row.SourceLockVersion.Valid || row.IntakeOperationID.Valid {
		return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement source is invalid")
	}
	canonical, err := json.Marshal(source)
	if err != nil {
		return requirementDelegationSource{}, true, fmt.Errorf("marshal native requirement source: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != normalizeRequirementDigest(row.SourceDigest) {
		return requirementDelegationSource{}, true, requirementSourceLinkUnavailable("native requirement source digest is invalid")
	}
	return source, true, nil
}

func (s *RequirementService) CreateRequirementDelegation(ctx context.Context, actor RequirementActor, input CreateRequirementDelegationRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	input.ExternalRef = strings.TrimSpace(input.ExternalRef)
	if input.ExternalRef == "" || len(input.ExternalRef) > 200 {
		return RequirementActionResult{}, requirementBadRequest("requirement_external_ref_invalid", "externalRef must contain 1–200 bytes")
	}
	squadID, err := util.ParseUUID(input.SquadID)
	if err != nil {
		return RequirementActionResult{}, requirementBadRequest("requirement_squad_invalid", "squadId must be a UUID")
	}
	input.SquadID = util.UUIDToString(squadID)
	projectID, err := optionalRequirementUUID(input.Candidate.ProjectID, "projectId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	input.Candidate.ProjectID = intakeNilString(projectID)
	participants, err := createRequirementParticipants(input.Participants, actor.UserID)
	if err != nil {
		return RequirementActionResult{}, err
	}
	hashBody := createRequirementDelegationHashBody{
		ExternalRef: input.ExternalRef, SquadID: input.SquadID,
		Candidate: input.Candidate, Participants: participants,
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, ActorType: "member", ActorID: actor.UserID,
		Action: "delegate", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementDelegationRoute, Body: hashBody,
	}, func(ctx context.Context, _ pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := requireRequirementMember(ctx, q, actor); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := validateRequirementParticipantMembers(ctx, q, actor.WorkspaceID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "squad_not_found", "squad not found")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement delegation squad: %w", err)
		}
		if squad.ArchivedAt.Valid {
			return RequirementActionResponse{}, requirementBadRequest("squad_not_available", "squad is archived")
		}
		leader, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: squad.LeaderID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "agent_not_found", "squad leader not found")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement delegation leader: %w", err)
		}
		if leader.ArchivedAt.Valid || !leader.RuntimeID.Valid {
			return RequirementActionResponse{}, requirementBadRequest("agent_not_available", "squad leader is archived or has no runtime")
		}
		if !CanMemberInvokeAgent(ctx, q, leader, actor.UserID, actor.WorkspaceID) {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "agent_invocation_not_allowed", "squad leader invocation is not allowed")
		}
		if projectID.Valid {
			if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: actor.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
				return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "project_not_found", "project not found")
			} else if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("load requirement delegation project: %w", err)
			}
		}
		source := requirementDelegationSource{
			Kind: requirementDelegationSourceKind, Title: strings.TrimSpace(input.Candidate.Goal),
			SquadID: input.SquadID, ExternalRef: input.ExternalRef,
		}
		sourceSnapshot, err := json.Marshal(source)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement delegation source: %w", err)
		}
		sourceDigest := sha256.Sum256(sourceSnapshot)
		row, err := q.CreateRequirement(ctx, db.CreateRequirementParams{
			WorkspaceID: actor.WorkspaceID, SourceHost: requirementDelegationSourceHost, ExternalID: input.ExternalRef,
			SourceUrl: "", SourceSnapshot: sourceSnapshot, SourceDigest: hex.EncodeToString(sourceDigest[:]),
			RequesterID: actor.UserID, AgentID: leader.ID, ProjectID: projectID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			existing, loadErr := q.GetRequirementBySource(ctx, db.GetRequirementBySourceParams{
				WorkspaceID: actor.WorkspaceID, SourceHost: requirementDelegationSourceHost, ExternalID: input.ExternalRef,
			})
			if loadErr != nil {
				return RequirementActionResponse{}, fmt.Errorf("load existing requirement delegation: %w", loadErr)
			}
			allowed, accessErr := s.requirementReadAuthorized(ctx, q, actor, existing)
			if accessErr != nil {
				return RequirementActionResponse{}, accessErr
			}
			conflict := newRequirementError(http.StatusConflict, "requirement_source_already_exists", "a requirement already exists for this externalRef")
			if allowed {
				conflict.ExistingRequirementID = util.UUIDToString(existing.ID)
			}
			return RequirementActionResponse{}, conflict
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement delegation: %w", err)
		}
		candidateRaw, err := json.Marshal(RequirementCandidateSnapshot{
			Goal: input.Candidate.Goal, NonGoals: input.Candidate.NonGoals, Scope: input.Candidate.Scope,
			Constraints: input.Candidate.Constraints, AcceptanceCriteria: input.Candidate.AcceptanceCriteria,
			ProjectID: input.Candidate.ProjectID, AgentID: util.UUIDToString(leader.ID),
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement delegation candidate: %w", err)
		}
		_, canonical, digest, err := CanonicalRequirementCandidateSnapshot(candidateRaw, row)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		candidate, err := q.CreateRequirementCandidate(ctx, db.CreateRequirementCandidateParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID, Revision: row.Revision + 1,
			Snapshot: canonical, Digest: digest,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement delegation candidate: %w", err)
		}
		updated, err := q.UpdateRequirementIntakeState(ctx, db.UpdateRequirementIntakeStateParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: row.Revision, IntakeState: "awaiting_approval",
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("advance requirement delegation state: %w", err)
		}
		if err := insertRequirementParticipants(ctx, q, actor.WorkspaceID, row.ID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", updated, "candidate_submitted", "", updated.IntakeState, map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "candidate_id": util.UUIDToString(candidate.ID),
			"squad_id": input.SquadID, "agent_id": util.UUIDToString(leader.ID), "project_id": requirementUUIDPtr(row.ProjectID),
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
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement delegation response: %w", err)
		}
		return RequirementActionResponse{
			RequirementID: row.ID, Status: http.StatusCreated, Body: body,
			EventType: "requirement:created", Realtime: detail.RealtimePointer(),
		}, nil
	})
}
