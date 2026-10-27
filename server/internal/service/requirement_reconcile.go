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

const requirementReconcileRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/reconcile"

var (
	ErrRequirementDeliveryRecoveryNotAllowed = errors.New("requirement delivery cannot be safely recovered")
	ErrRequirementDeliveryRequestHash        = errors.New("requirement delivery request hash mismatch")
)

type ReconcileRequirementRequest struct {
	OperationID *string `json:"operationId,omitempty"`
	DeliveryID  *string `json:"deliveryId,omitempty"`
}

type reconcileRequirementHashBody struct {
	OperationID string `json:"operationId"`
	DeliveryID  string `json:"deliveryId"`
}

func (s *RequirementService) ReconcileRequirement(ctx context.Context, actor RequirementActor, requirementID pgtype.UUID, input ReconcileRequirementRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.OperationID == nil && input.DeliveryID == nil {
		return RequirementActionResult{}, requirementBadRequest("reconcile_target_required", "operationId or deliveryId is required")
	}
	operationID, err := optionalRequirementUUID(input.OperationID, "operationId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	deliveryID, err := optionalRequirementUUID(input.DeliveryID, "deliveryId")
	if err != nil {
		return RequirementActionResult{}, err
	}
	body := reconcileRequirementHashBody{}
	if operationID.Valid {
		body.OperationID = util.UUIDToString(operationID)
	}
	if deliveryID.Valid {
		body.DeliveryID = util.UUIDToString(deliveryID)
	}
	actionInput := RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "member", ActorID: actor.UserID,
		Action: "reconcile", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementReconcileRoute, Body: body,
		AuthorizeReplay: func(ctx context.Context, q *db.Queries, requirementID pgtype.UUID) error {
			return requireRequirementReconcileActor(ctx, q, actor, requirementID)
		},
	}
	requestHash, err := RequirementActionHash(actionInput)
	if err != nil {
		return RequirementActionResult{}, err
	}
	actionInput.RequestHash = requestHash
	return s.ExecuteAction(ctx, actionInput, func(ctx context.Context, _ pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement reconcile: %w", err)
		}
		if err := requireRequirementReconcileActor(ctx, q, actor, row.ID); err != nil {
			return RequirementActionResponse{}, err
		}
		var operation db.YixiezuoOperation
		if operationID.Valid {
			operation, err = q.GetYixiezuoOperationInWorkspace(ctx, db.GetYixiezuoOperationInWorkspaceParams{WorkspaceID: actor.WorkspaceID, ID: operationID})
			if errors.Is(err, pgx.ErrNoRows) {
				return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "reconcile_operation_not_found", "reconcile operation not found")
			}
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("load reconcile operation: %w", err)
			}
			if !requirementOwnsOperation(row, operation) {
				return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "reconcile_operation_forbidden", "operation does not belong to this requirement")
			}
			if operation.Kind != "publish" {
				return RequirementActionResponse{}, requirementBadRequest("reconcile_operation_not_publish", "only an existing publish operation can be reconciled")
			}
		}
		var delivery db.PopoBridgeCommand
		if deliveryID.Valid {
			delivery, err = q.GetRequirementPopoCommandByDeliveryID(ctx, db.GetRequirementPopoCommandByDeliveryIDParams{WorkspaceID: actor.WorkspaceID, DeliveryID: deliveryID})
			if errors.Is(err, pgx.ErrNoRows) {
				return RequirementActionResponse{}, newRequirementError(http.StatusNotFound, "reconcile_delivery_not_found", "reconcile delivery not found")
			}
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("load reconcile delivery: %w", err)
			}
			if delivery.RequirementID != row.ID {
				return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "reconcile_delivery_forbidden", "delivery does not belong to this requirement")
			}
			if operationID.Valid && (!delivery.OperationID.Valid || delivery.OperationID != operationID) {
				return RequirementActionResponse{}, requirementBadRequest("reconcile_target_mismatch", "operationId and deliveryId do not identify the same operation")
			}
		}
		if operationID.Valid {
			operationPayload := decodeOperationPayload(operation.Payload)
			payload, err := json.Marshal(yixiezuo.OperationPayload{
				Source: operationPayload.Source, ExpectedDigest: operationPayload.ExpectedDigest,
				StatusName: operationPayload.StatusName, Revision: operationPayload.Revision, TargetOperationID: util.UUIDToString(operation.ID),
			})
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("marshal reconcile operation: %w", err)
			}
			reconcile, err := q.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{
				WorkspaceID: actor.WorkspaceID, RequestedBy: actor.UserID, Kind: "reconcile", IssueID: operation.IssueID,
				Payload: payload, RequestKey: pgtype.Text{String: "requirement-reconcile:" + strings.TrimSpace(idempotencyKey), Valid: true},
				RequirementID: row.ID, TargetOperationID: operation.ID, RequestHash: pgtype.Text{String: requestHash, Valid: true},
			})
			if err != nil {
				return RequirementActionResponse{}, fmt.Errorf("create reconcile operation: %w", err)
			}
			if reconcile.Kind != "reconcile" || reconcile.RequirementID != row.ID || reconcile.TargetOperationID != operation.ID || reconcile.RequestHash.String != requestHash {
				return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_action_conflict", "the Idempotency-Key already identifies a different reconcile")
			}
		}
		if deliveryID.Valid {
			if s.DeliveryReconciler == nil {
				return RequirementActionResponse{}, newRequirementError(http.StatusServiceUnavailable, "popo_not_configured", "POPO delivery reconcile is unavailable")
			}
			delivery, err = s.DeliveryReconciler.ReconcileRequirementDelivery(ctx, q, actor.WorkspaceID, deliveryID)
			if err != nil {
				return RequirementActionResponse{}, reconcileDeliveryError(err)
			}
			if delivery.Status == "failed" {
				if _, err = s.DeliveryReconciler.RecoverConfirmedFailedRequirementDelivery(ctx, q, actor.WorkspaceID, deliveryID); err != nil {
					return RequirementActionResponse{}, reconcileDeliveryError(err)
				}
			}
		}
		return s.requirementActionDetailResponse(ctx, q, actor, row, RequirementActionResponse{})
	})
}

func requireRequirementReconcileActor(ctx context.Context, q *db.Queries, actor RequirementActor, requirementID pgtype.UUID) error {
	_, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: actor.UserID, WorkspaceID: actor.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return requirementForbidden()
	}
	if err != nil {
		return fmt.Errorf("load requirement reconcile member: %w", err)
	}
	roles, err := q.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, UserID: actor.UserID,
	})
	if err != nil {
		return fmt.Errorf("list requirement reconcile roles: %w", err)
	}
	for _, role := range roles {
		if role.Role == "approver" {
			return nil
		}
	}
	return requirementForbidden()
}

func requirementOwnsOperation(row db.Requirement, operation db.YixiezuoOperation) bool {
	if operation.RequirementID.Valid {
		return operation.RequirementID == row.ID
	}
	if row.IntakeOperationID.Valid && row.IntakeOperationID == operation.ID {
		return true
	}
	return operation.IssueID.Valid && row.IssueID.Valid && operation.IssueID == row.IssueID
}

func decodeOperationPayload(raw []byte) yixiezuo.OperationPayload {
	var payload yixiezuo.OperationPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}

func reconcileDeliveryError(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return newRequirementError(http.StatusNotFound, "reconcile_delivery_not_found", "reconcile delivery not found")
	case errors.Is(err, ErrRequirementDeliveryRequestHash):
		return newRequirementError(http.StatusConflict, "reconcile_delivery_hash_mismatch", "delivery identity hash does not match")
	case errors.Is(err, ErrRequirementDeliveryRecoveryNotAllowed):
		return newRequirementError(http.StatusConflict, "reconcile_delivery_recovery_not_allowed", "delivery cannot be safely recovered")
	default:
		return newRequirementError(http.StatusConflict, "reconcile_delivery_failed", "delivery could not be reconciled").withCause(err)
	}
}

func (e *RequirementError) withCause(cause error) *RequirementError {
	e.Cause = cause
	return e
}
