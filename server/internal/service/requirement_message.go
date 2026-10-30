package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type SendRequirementMessageRequest struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Content          string `json:"content"`
}

func (s *RequirementService) SendRequirementMessage(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input SendRequirementMessageRequest, key, requestID string) (RequirementActionResult, error) {
	input.Content = strings.TrimSpace(util.SanitizeTextForPostgres(input.Content))
	if input.ExpectedRevision < 1 || input.Content == "" || len(input.Content) > 20000 {
		return RequirementActionResult{}, requirementBadRequest("requirement_message_invalid", "a revision and 1-20000 bytes of content are required")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement message: task service is required")
	}
	var prepared PreparedChatTaskEnqueue
	var sessionID pgtype.UUID
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "message", IdempotencyKey: key, Method: http.MethodPost, Route: "POST /api/workspaces/{id}/requirements/{requirementId}/messages",
		ExpectedRevision: &input.ExpectedRevision, Body: input,
		Prepare: func(ctx context.Context) error {
			row, err := s.Queries.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
			if errors.Is(err, pgx.ErrNoRows) {
				return requirementNotFound()
			}
			if err != nil {
				return err
			}
			member, roles, err := s.requirementMemberAndRoles(ctx, s.Queries, actor, row.ID)
			if err != nil {
				return err
			}
			if !member || (!roles["requester"] && !roles["approver"]) {
				return requirementForbidden()
			}
			if row.IssueID.Valid || row.CancelledAt.Valid || !row.ChatSessionID.Valid {
				return newRequirementError(http.StatusConflict, "requirement_chat_closed", "requirement is not open for planning")
			}
			sessionID = row.ChatSessionID
			prepared, err = s.Tasks.PrepareChatTaskEnqueue(ctx, row.AgentID, actor.UserID)
			return err
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		// Public Chat sends acquire session before Requirement; keep that order.
		_, err := q.LockChatSessionForRuntimeBind(ctx, sessionID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		session, err := q.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: sessionID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || (!roles["requester"] && !roles["approver"]) {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if row.Revision != input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, input.ExpectedRevision, row.Revision)
		}
		if row.IssueID.Valid || row.CancelledAt.Valid || row.ChatSessionID != sessionID {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_chat_closed", "requirement is not open for planning")
		}
		agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: row.AgentID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if agent.ArchivedAt.Valid || !CanMemberInvokeAgent(ctx, q, agent, actor.UserID, actor.WorkspaceID) {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if err := validateRequirementPlanRuntime(ctx, q, agent); err != nil {
			return RequirementActionResponse{}, err
		}
		sent, err := s.Tasks.SendPreparedRequirementChatMessageInTx(ctx, tx, session, actor.UserID, input.Content, prepared)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		updated, err := q.UpdateRequirementIntakeState(ctx, db.UpdateRequirementIntakeStateParams{ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: input.ExpectedRevision, IntakeState: "clarifying"})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", updated, "message", row.IntakeState, "clarifying", map[string]any{"message_id": util.UUIDToString(sent.Message.ID), "task_id": util.UUIDToString(sent.Task.ID)}, requestID)
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
			return RequirementActionResponse{}, err
		}
		return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusCreated, Body: body, EventType: protocol.EventRequirementUpdated, Realtime: detail.RealtimePointer(), FinalizeTasks: []db.AgentTaskQueue{sent.Task}, ChatMessages: []db.ChatMessage{sent.Message}}, nil
	})
}
