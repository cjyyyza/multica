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
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/text"
)

const requirementOpenRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/open"

type OpenRequirementRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

func validateRequirementPlanRuntime(ctx context.Context, q *db.Queries, agentRow db.Agent) error {
	if !agentRow.RuntimeID.Valid {
		return newRequirementError(http.StatusConflict, "plan_mode_not_supported", "Plan Mode requires a runtime")
	}
	runtime, err := q.GetAgentRuntime(ctx, agentRow.RuntimeID)
	if err != nil {
		return fmt.Errorf("load requirement plan runtime: %w", err)
	}
	if runtime.ProfileID.Valid || !agent.SupportsPlanMode(runtime.Provider) {
		return newRequirementError(http.StatusConflict, "plan_mode_not_supported", "Plan Mode is not supported for this runtime provider")
	}
	return nil
}

type RequirementOpenResponseBuilder func(detail RequirementDetail, session db.ChatSession, task *db.AgentTaskQueue) (json.RawMessage, error)

func (s *RequirementService) OpenRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input OpenRequirementRequest, idempotencyKey, requestID string, buildResponse RequirementOpenResponseBuilder) (RequirementActionResult, error) {
	if input.ExpectedRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_invalid", "expectedRevision must be positive")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement open: task service is required")
	}
	if buildResponse == nil {
		return RequirementActionResult{}, errors.New("requirement open: response builder is required")
	}
	var prepared PreparedChatTaskEnqueue
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "open", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementOpenRoute,
		ExpectedRevision: &input.ExpectedRevision, Body: input,
		Prepare: func(ctx context.Context) error {
			row, err := s.Queries.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
			if err != nil {
				return err
			}
			if row.ChatSessionID.Valid {
				return nil
			}
			if err := s.requireFlow(ctx); err != nil {
				return err
			}
			prepared, err = s.Tasks.PrepareChatTaskEnqueue(ctx, row.AgentID, actor.UserID)
			return err
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement open: %w", err)
		}
		if row.Revision != input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, input.ExpectedRevision, row.Revision)
		}
		member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if !member || len(roles) == 0 {
			return RequirementActionResponse{}, requirementForbidden()
		}
		if row.CancelledAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is cancelled")
		}
		if row.IssueID.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementChatIssueExistsCode, "requirement already has an issue")
		}
		if row.ChatSessionID.Valid {
			return s.openExistingRequirement(ctx, q, actor, row, buildResponse)
		}
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		if !validateStoredRequirementSource(row) {
			return RequirementActionResponse{}, requirementBadRequest("requirement_source_invalid", "a valid source snapshot and digest are required")
		}
		agentRow, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: row.AgentID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "agent_not_found", "agent not found")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("load requirement open agent: %w", err)
		}
		if agentRow.ArchivedAt.Valid {
			return RequirementActionResponse{}, requirementBadRequest("agent_not_available", "agent is archived")
		}
		if !CanMemberInvokeAgent(ctx, q, agentRow, actor.UserID, actor.WorkspaceID) {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "agent_invocation_not_allowed", "agent invocation is not allowed")
		}
		if row.ProjectID.Valid {
			if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: row.ProjectID, WorkspaceID: actor.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
				return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "project_not_found", "project not found")
			} else if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("load requirement open project: %w", err)
			}
		}
		if err := validateRequirementPlanRuntime(ctx, q, agentRow); err != nil {
			return RequirementActionResponse{}, err
		}

		var snapshot yixiezuo.Snapshot
		if err := json.Unmarshal(row.SourceSnapshot, &snapshot); err != nil {
			return RequirementActionResponse{}, requirementBadRequest("requirement_source_invalid", "invalid source snapshot")
		}
		session, err := q.CreateChatSession(ctx, db.CreateChatSessionParams{
			WorkspaceID: actor.WorkspaceID, AgentID: row.AgentID, CreatorID: actor.UserID,
			Title: snapshot.Title, IsAgentIntro: false, ProjectID: row.ProjectID, ID: dbid.NewV7(),
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement chat: %w", err)
		}
		session, err = q.MarkChatSessionExplicitlyCreated(ctx, session.ID)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("mark requirement chat visible: %w", err)
		}
		bound, err := q.BindRequirementChatSession(ctx, db.BindRequirementChatSessionParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: input.ExpectedRevision, ChatSessionID: session.ID,
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("bind requirement chat: %w", err)
		}
		task, err := s.Tasks.EnqueuePreparedRequirementChatTaskInTx(ctx, tx, session, actor.UserID, prepared)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("enqueue requirement chat task: %w", err)
		}
		if err := createRequirementOpeningMessages(ctx, q, bound, session, task, snapshot, requirementOpeningLocale(ctx, q, actor.UserID)); err != nil {
			return RequirementActionResponse{}, err
		}
		event, err := appendRequirementEvent(ctx, q, actor, "member", bound, "opened", row.IntakeState, bound.IntakeState, map[string]any{
			"requirement_id": util.UUIDToString(bound.ID), "chat_session_id": util.UUIDToString(session.ID), "task_id": util.UUIDToString(task.ID),
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
		body, err := buildResponse(detail, session, &task)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement open response: %w", err)
		}
		return RequirementActionResponse{
			RequirementID: bound.ID, Status: http.StatusOK, Body: body,
			EventType: protocol.EventRequirementUpdated, Realtime: detail.RealtimePointer(), FinalizeTasks: []db.AgentTaskQueue{task},
		}, nil
	})
}

func (s *RequirementService) openExistingRequirement(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, buildResponse RequirementOpenResponseBuilder) (RequirementActionResponse, error) {
	session, err := q.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: row.ChatSessionID, WorkspaceID: actor.WorkspaceID})
	if err != nil {
		return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_open_incomplete", "requirement chat is missing")
	}
	task, err := q.GetRequirementInitialChatTask(ctx, db.GetRequirementInitialChatTaskParams{
		RequirementID: row.ID, ChatSessionID: row.ChatSessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_open_incomplete", "requirement chat has no initial task")
	}
	if err != nil {
		return RequirementActionResponse{}, fmt.Errorf("load requirement initial task: %w", err)
	}
	detail, err := s.BuildDetail(ctx, q, actor, row, RequirementDetailOptions{ReadAuthorized: true})
	if err != nil {
		return RequirementActionResponse{}, err
	}
	body, err := buildResponse(detail, session, &task)
	if err != nil {
		return RequirementActionResponse{}, fmt.Errorf("marshal requirement open response: %w", err)
	}
	return RequirementActionResponse{RequirementID: row.ID, Status: http.StatusOK, Body: body}, nil
}

// createRequirementOpeningMessages writes the two product-authored rows every
// requirement chat starts with: a hidden source-context frame consumed by the
// runtime, and the visible opening in the member's language.
//
// The rows keep separate kinds on purpose. requirement_context is an input
// frame, requirement_opening is rendered to the member, and onboarding copy is
// a different product surface — reusing onboarding_opening here would change
// what the requirement chat UI renders.
func createRequirementOpeningMessages(ctx context.Context, q *db.Queries, row db.Requirement, session db.ChatSession, task db.AgentTaskQueue, snapshot yixiezuo.Snapshot, locale text.Locale) error {
	contextBody, err := json.Marshal(map[string]any{
		"schema_version": 1, "requirement_id": util.UUIDToString(row.ID),
		"source_snapshot": json.RawMessage(row.SourceSnapshot), "source_digest": snapshot.Digest,
	})
	if err != nil {
		return fmt.Errorf("marshal requirement source context: %w", err)
	}
	if _, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID: dbid.NewV7(), ChatSessionID: session.ID, Role: "user", Content: string(contextBody), TaskID: task.ID,
		MessageKind: pgtype.Text{String: protocol.ChatMessageKindRequirementContext, Valid: true},
	}); err != nil {
		return fmt.Errorf("create requirement source context: %w", err)
	}
	opening := text.BuildRequirementOpening(locale, snapshot.Title)
	if _, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID: dbid.NewV7(), ChatSessionID: session.ID, Role: "assistant", Content: opening, TaskID: task.ID,
		MessageKind: pgtype.Text{String: protocol.ChatMessageKindRequirementOpening, Valid: true},
	}); err != nil {
		return fmt.Errorf("create requirement opening: %w", err)
	}
	return nil
}

// requirementOpeningLocale resolves the acting member's profile language for
// the product-authored opening. It runs on the action's transaction and adds
// no write. A missing user row or an unreadable language falls back to English
// rather than failing the open: an opening in the fallback language is more
// useful than none, and a real transaction failure surfaces on the next
// statement anyway.
func requirementOpeningLocale(ctx context.Context, q *db.Queries, userID pgtype.UUID) text.Locale {
	if !userID.Valid {
		return text.FallbackLocale
	}
	user, err := q.GetUser(ctx, userID)
	if err != nil {
		return text.FallbackLocale
	}
	return text.ResolveLocale(user.Language.String)
}
