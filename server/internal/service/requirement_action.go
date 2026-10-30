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
)

type RequirementActionInput struct {
	WorkspaceID      pgtype.UUID
	RequirementID    pgtype.UUID
	ActorType        string
	ActorID          pgtype.UUID
	Action           string
	IdempotencyKey   string
	Method           string
	Route            string
	ExpectedRevision *int64
	Body             any
	// RequestHash carries a hash the caller already computed for this input;
	// ExecuteAction computes one when it is empty.
	RequestHash     string
	Prepare         func(context.Context) error
	AuthorizeReplay func(context.Context, *db.Queries, pgtype.UUID) error
}

type RequirementIssueUpdate struct {
	Issue          db.Issue
	PreviousStatus string
}

type RequirementCommentEvent struct {
	Issue         db.Issue
	Comment       db.Comment
	IssueRevision int64
}

type RequirementActionResponse struct {
	RequirementID  pgtype.UUID
	Status         int
	Body           json.RawMessage
	EventType      string
	Realtime       *RequirementRealtimePointer
	FinalizeTasks  []db.AgentTaskQueue
	PromoteTasks   []db.AgentTaskQueue
	IssueUpdates   []RequirementIssueUpdate
	Comments       []RequirementCommentEvent
	ChatMessages   []db.ChatMessage
	CancelledTasks []db.AgentTaskQueue
	PostCommit     func()
}

type RequirementActionFunc func(context.Context, pgx.Tx, *db.Queries) (RequirementActionResponse, error)

type requirementActionHashEnvelope struct {
	Method           string  `json:"method"`
	Route            string  `json:"route"`
	WorkspaceID      string  `json:"workspaceId"`
	RequirementID    *string `json:"requirementId"`
	ExpectedRevision *int64  `json:"expectedRevision"`
	Body             any     `json:"body"`
}

func RequirementActionHash(input RequirementActionInput) (string, error) {
	encoded, err := json.Marshal(requirementActionHashEnvelope{
		Method: input.Method, Route: input.Route,
		WorkspaceID: util.UUIDToString(input.WorkspaceID), RequirementID: requirementUUIDPtr(input.RequirementID),
		ExpectedRevision: input.ExpectedRevision, Body: input.Body,
	})
	if err != nil {
		return "", fmt.Errorf("marshal requirement action hash: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *RequirementService) ExecuteAction(ctx context.Context, input RequirementActionInput, fn RequirementActionFunc) (RequirementActionResult, error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" {
		return RequirementActionResult{}, requirementBadRequest("idempotency_key_required", "A non-empty Idempotency-Key is required")
	}
	if len(input.IdempotencyKey) > 512 {
		return RequirementActionResult{}, requirementBadRequest("idempotency_key_too_long", "Idempotency-Key is too long")
	}
	if input.Method == "" || input.Route == "" || input.Action == "" || input.ActorType == "" || !input.WorkspaceID.Valid || !input.ActorID.Valid {
		return RequirementActionResult{}, requirementBadRequest("invalid_requirement_action", "invalid requirement action")
	}
	requestHash := input.RequestHash
	if requestHash == "" {
		var err error
		requestHash, err = RequirementActionHash(input)
		if err != nil {
			return RequirementActionResult{}, err
		}
	}
	if replay, ok, replayErr := s.tryReplayRequirementAction(ctx, input, requestHash); replayErr != nil {
		return RequirementActionResult{}, replayErr
	} else if ok {
		return replay, nil
	}
	if input.Prepare != nil {
		if err := input.Prepare(ctx); err != nil {
			return RequirementActionResult{}, err
		}
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return RequirementActionResult{}, fmt.Errorf("begin requirement action: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)

	started, err := q.BeginRequirementAction(ctx, db.BeginRequirementActionParams{
		WorkspaceID: input.WorkspaceID, RequirementID: input.RequirementID,
		ActorType: input.ActorType, ActorID: input.ActorID, Action: input.Action,
		IdempotencyKey: input.IdempotencyKey, RequestHash: requestHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.replayRequirementAction(ctx, q, input, requestHash)
	}
	if err != nil {
		return RequirementActionResult{}, fmt.Errorf("begin requirement action row: %w", err)
	}

	response, err := fn(ctx, tx, q)
	if err != nil {
		return RequirementActionResult{}, err
	}
	if response.Status < 100 || response.Status > 599 || !json.Valid(response.Body) {
		return RequirementActionResult{}, errors.New("requirement action returned an invalid response")
	}
	if _, err = q.CompleteRequirementAction(ctx, db.CompleteRequirementActionParams{
		RequirementID:  response.RequirementID,
		ResponseStatus: pgtype.Int4{Int32: int32(response.Status), Valid: true},
		ResponseBody:   response.Body,
		ID:             started.ID, WorkspaceID: input.WorkspaceID, RequestHash: requestHash,
	}); err != nil {
		return RequirementActionResult{}, fmt.Errorf("complete requirement action: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return RequirementActionResult{}, fmt.Errorf("commit requirement action: %w", err)
	}
	return RequirementActionResult{
		Status: response.Status, Body: response.Body,
		EventType: response.EventType, Realtime: response.Realtime, FinalizeTasks: response.FinalizeTasks, PromoteTasks: response.PromoteTasks,
		IssueUpdates: response.IssueUpdates, Comments: response.Comments, ChatMessages: response.ChatMessages, CancelledTasks: response.CancelledTasks, PostCommit: response.PostCommit,
		WorkspaceID: util.UUIDToString(input.WorkspaceID), ActorType: input.ActorType, ActorID: util.UUIDToString(input.ActorID),
	}, nil
}

func (s *RequirementService) tryReplayRequirementAction(ctx context.Context, input RequirementActionInput, requestHash string) (RequirementActionResult, bool, error) {
	existing, err := s.Queries.GetRequirementAction(ctx, db.GetRequirementActionParams{
		WorkspaceID: input.WorkspaceID, ActorType: input.ActorType, ActorID: input.ActorID,
		Action: input.Action, IdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementActionResult{}, false, nil
	}
	if err != nil {
		return RequirementActionResult{}, false, fmt.Errorf("load requirement action replay: %w", err)
	}
	result, err := s.finishRequirementActionReplay(ctx, s.Queries, input, existing, requestHash)
	return result, err == nil, err
}

func (s *RequirementService) replayRequirementAction(ctx context.Context, q *db.Queries, input RequirementActionInput, requestHash string) (RequirementActionResult, error) {
	existing, err := q.LockRequirementAction(ctx, db.LockRequirementActionParams{
		WorkspaceID: input.WorkspaceID, ActorType: input.ActorType, ActorID: input.ActorID,
		Action: input.Action, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return RequirementActionResult{}, fmt.Errorf("load requirement action replay: %w", err)
	}
	return s.finishRequirementActionReplay(ctx, q, input, existing, requestHash)
}

func (s *RequirementService) finishRequirementActionReplay(ctx context.Context, q *db.Queries, input RequirementActionInput, existing db.RequirementAction, requestHash string) (RequirementActionResult, error) {
	authorize := input.AuthorizeReplay
	if authorize == nil {
		actor := RequirementActor{WorkspaceID: input.WorkspaceID, UserID: input.ActorID}
		authorize = func(ctx context.Context, q *db.Queries, requirementID pgtype.UUID) error {
			return s.authorizeRequirementReplay(ctx, q, actor, requirementID)
		}
	}
	if err := authorize(ctx, q, existing.RequirementID); err != nil {
		return RequirementActionResult{}, err
	}
	if existing.RequestHash != requestHash {
		return RequirementActionResult{}, newRequirementError(http.StatusConflict, "requirement_action_conflict", "the Idempotency-Key already identifies a different request")
	}
	if !existing.ResponseStatus.Valid || !json.Valid(existing.ResponseBody) {
		return RequirementActionResult{}, errors.New("completed requirement action has no valid stored response")
	}
	return RequirementActionResult{
		Status: int(existing.ResponseStatus.Int32), Body: existing.ResponseBody, Replay: true,
	}, nil
}

func (s *RequirementService) authorizeRequirementReplay(ctx context.Context, q *db.Queries, actor RequirementActor, requirementID pgtype.UUID) error {
	if !requirementID.Valid {
		return errors.New("completed requirement action has no requirement")
	}
	row, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return requirementNotFound()
	}
	if err != nil {
		return fmt.Errorf("load requirement replay access: %w", err)
	}
	allowed, err := s.requirementReadAuthorized(ctx, q, actor, row)
	if err != nil {
		return err
	}
	if !allowed {
		return requirementForbidden()
	}
	return nil
}
