package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const requirementRequestRoute = "POST /api/workspaces/{id}/requirement-requests"

type CreateNativeRequirementRequest struct {
	ExternalRef  string                        `json:"externalRef"`
	Idea         string                        `json:"idea"`
	AgentID      string                        `json:"agentId,omitempty"`
	SquadID      string                        `json:"squadId,omitempty"`
	ProjectID    *string                       `json:"projectId"`
	Participants []RequirementParticipantInput `json:"participants"`
}

func requirementRequestAgent(ctx context.Context, q *db.Queries, actor RequirementActor, input CreateNativeRequirementRequest) (db.Agent, error) {
	agentID, err := optionalRequirementUUID(&input.AgentID, "agentId")
	if err != nil {
		return db.Agent{}, err
	}
	if input.SquadID != "" {
		squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: util.MustParseUUID(input.SquadID), WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Agent{}, newRequirementError(http.StatusNotFound, "squad_not_found", "squad not found")
		}
		if err != nil {
			return db.Agent{}, err
		}
		if squad.ArchivedAt.Valid {
			return db.Agent{}, newRequirementError(http.StatusConflict, "squad_not_available", "squad is archived")
		}
		agentID = squad.LeaderID
	}
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Agent{}, newRequirementError(http.StatusNotFound, "agent_not_found", "agent not found")
	}
	if err != nil {
		return db.Agent{}, err
	}
	if agent.ArchivedAt.Valid || !CanMemberInvokeAgent(ctx, q, agent, actor.UserID, actor.WorkspaceID) {
		return db.Agent{}, newRequirementError(http.StatusForbidden, "agent_invocation_not_allowed", "agent invocation is not allowed")
	}
	if err := validateRequirementPlanRuntime(ctx, q, agent); err != nil {
		return db.Agent{}, err
	}
	return agent, nil
}

func (s *RequirementService) CreateNativeRequirement(ctx context.Context, actor RequirementActor, input CreateNativeRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	input.ExternalRef, input.Idea = strings.TrimSpace(util.SanitizeTextForPostgres(input.ExternalRef)), strings.TrimSpace(util.SanitizeTextForPostgres(input.Idea))
	input.AgentID, input.SquadID = strings.TrimSpace(input.AgentID), strings.TrimSpace(input.SquadID)
	if input.ExternalRef == "" || len(input.ExternalRef) > 200 {
		return RequirementActionResult{}, requirementBadRequest("requirement_external_ref_invalid", "externalRef must contain 1-200 bytes")
	}
	if input.Idea == "" || len(input.Idea) > 20000 {
		return RequirementActionResult{}, requirementBadRequest("requirement_idea_invalid", "idea must contain 1-20000 bytes")
	}
	if (input.AgentID == "") == (input.SquadID == "") {
		return RequirementActionResult{}, requirementBadRequest("requirement_target_invalid", "provide exactly one of agentId or squadId")
	}
	for field, target := range map[string]*string{"agentId": &input.AgentID, "squadId": &input.SquadID} {
		if *target == "" {
			continue
		}
		id, err := util.ParseUUID(*target)
		if err != nil {
			return RequirementActionResult{}, requirementBadRequest("requirement_target_invalid", field+" must be a UUID")
		}
		*target = util.UUIDToString(id)
	}
	projectID, err := optionalRequirementUUID(input.ProjectID, "projectId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	input.ProjectID = intakeNilString(projectID)
	participants, err := createRequirementParticipants(input.Participants, actor.UserID)
	if err != nil {
		return RequirementActionResult{}, err
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement request: task service is required")
	}
	var prepared PreparedChatTaskEnqueue
	var preparedAgentID pgtype.UUID
	hashBody := struct {
		CreateNativeRequirementRequest
		Participants []normalizedRequirementParticipant `json:"participants"`
	}{input, participants}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, ActorType: "member", ActorID: actor.UserID,
		Action: "request", IdempotencyKey: idempotencyKey, Method: http.MethodPost, Route: requirementRequestRoute, Body: hashBody,
		Prepare: func(ctx context.Context) error {
			if err := s.requireFlow(ctx); err != nil {
				return err
			}
			if err := requireRequirementMember(ctx, s.Queries, actor); err != nil {
				return err
			}
			agent, err := requirementRequestAgent(ctx, s.Queries, actor, input)
			if err != nil {
				return err
			}
			preparedAgentID = agent.ID
			prepared, err = s.Tasks.PrepareChatTaskEnqueue(ctx, agent.ID, actor.UserID)
			return err
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := requireRequirementMember(ctx, q, actor); err != nil {
			return RequirementActionResponse{}, err
		}
		if err := validateRequirementParticipantMembers(ctx, q, actor.WorkspaceID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		agent, err := requirementRequestAgent(ctx, q, actor, input)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if agent.ID != preparedAgentID {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_target_changed", "requirement target changed; review it before retrying")
		}
		kind := requirementAgentRequestKind
		if input.SquadID != "" {
			kind = requirementSquadRequestKind
		}
		title := []rune(strings.SplitN(input.Idea, "\n", 2)[0])
		if len(title) > 140 {
			title = title[:140]
		}
		source := requirementDelegationSource{Kind: kind, Title: strings.TrimSpace(string(title)), SquadID: input.SquadID, ExternalRef: input.ExternalRef, Idea: input.Idea, AgentID: input.AgentID}
		raw, err := json.Marshal(source)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		digest := sha256.Sum256(raw)
		row, err := q.CreateRequirement(ctx, db.CreateRequirementParams{
			WorkspaceID: actor.WorkspaceID, SourceHost: requirementDelegationSourceHost, ExternalID: input.ExternalRef,
			SourceSnapshot: raw, SourceDigest: hex.EncodeToString(digest[:]), RequesterID: actor.UserID, AgentID: agent.ID, ProjectID: projectID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			conflict := newRequirementError(http.StatusConflict, "requirement_source_already_exists", "a requirement already exists for this externalRef")
			existing, loadErr := q.GetRequirementBySource(ctx, db.GetRequirementBySourceParams{WorkspaceID: actor.WorkspaceID, SourceHost: requirementDelegationSourceHost, ExternalID: input.ExternalRef})
			if loadErr != nil {
				return RequirementActionResponse{}, loadErr
			}
			allowed, accessErr := s.requirementReadAuthorized(ctx, q, actor, existing)
			if accessErr != nil {
				return RequirementActionResponse{}, accessErr
			}
			if allowed {
				conflict.ExistingRequirementID = util.UUIDToString(existing.ID)
			}
			return RequirementActionResponse{}, conflict
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create native requirement: %w", err)
		}
		if err := insertRequirementParticipants(ctx, q, actor.WorkspaceID, row.ID, participants); err != nil {
			return RequirementActionResponse{}, err
		}
		bound, session, task, err := s.createRequirementPlanChat(ctx, tx, q, actor, row, prepared)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", bound, "opened", row.IntakeState, bound.IntakeState, map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "chat_session_id": util.UUIDToString(session.ID), "task_id": util.UUIDToString(task.ID),
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
		body, err := json.Marshal(detail)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusCreated, Body: body, EventType: protocol.EventRequirementCreated, Realtime: detail.RealtimePointer(), FinalizeTasks: []db.AgentTaskQueue{task}}, nil
	})
}
