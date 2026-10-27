package popo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrRequirementRecoveryNotAllowed = service.ErrRequirementDeliveryRecoveryNotAllowed
	ErrRequirementRequestHash        = service.ErrRequirementDeliveryRequestHash
)

// ReconcileRequirementDelivery reads the ledger row and clears the operator
// hint once its outcome is settled. The command row is already the delivery
// projection, so there is no second state store to repair.
func (w *RequirementOutboxWorker) ReconcileRequirementDelivery(ctx context.Context, q *db.Queries, workspaceID, deliveryID pgtype.UUID) (db.PopoBridgeCommand, error) {
	if err := q.ExpireRequirementPopoCommandLeases(ctx); err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if err := q.ExpireUndispatchedRequirementPopoCommands(ctx); err != nil {
		return db.PopoBridgeCommand{}, err
	}
	row, err := q.GetRequirementPopoCommandByDeliveryID(ctx, db.GetRequirementPopoCommandByDeliveryIDParams{WorkspaceID: workspaceID, DeliveryID: deliveryID})
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if !row.NeedsReconcile {
		return row, nil
	}
	if row.Status == CommandStatusDelivered || row.Status == CommandStatusSuperseded {
		cleared, err := q.ClearRequirementPopoCommandReconcile(ctx, db.ClearRequirementPopoCommandReconcileParams{ID: row.ID, WorkspaceID: workspaceID})
		if err != nil {
			return db.PopoBridgeCommand{}, err
		}
		return cleared, nil
	}
	return row, nil
}

// RecoverConfirmedFailedRequirementDelivery requeues only a delivery whose own
// evidence proves it was not sent, whose payload has not drifted, and whose
// bridge supports receipt attempt fencing.
func (w *RequirementOutboxWorker) RecoverConfirmedFailedRequirementDelivery(ctx context.Context, q *db.Queries, workspaceID, deliveryID pgtype.UUID) (db.PopoBridgeCommand, error) {
	row, err := q.GetRequirementPopoCommandByDeliveryID(ctx, db.GetRequirementPopoCommandByDeliveryIDParams{WorkspaceID: workspaceID, DeliveryID: deliveryID})
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if row.Status != CommandStatusFailed || row.SupersededAt.Valid || !row.NotSent || !row.Retryable || !row.EvidenceFinalized ||
		row.Attempt >= requirementOutboxMaxTransportTries || !row.BridgeID.Valid {
		return db.PopoBridgeCommand{}, ErrRequirementRecoveryNotAllowed
	}
	if !row.PayloadHash.Valid || row.PayloadHash.String != requirementPayloadHash(row.Payload) {
		return db.PopoBridgeCommand{}, ErrRequirementRequestHash
	}
	expectedHash := service.RequirementDeliveryRequestHash(service.RequirementDeliveryIdentityFromCommand(row))
	if !row.RequestHash.Valid || row.RequestHash.String != expectedHash {
		return db.PopoBridgeCommand{}, ErrRequirementRequestHash
	}
	bridge, err := q.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{ID: row.BridgeID, WorkspaceID: workspaceID})
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if !BridgeSupportsReceiptAttempt(bridge) {
		return db.PopoBridgeCommand{}, ErrRequirementRecoveryNotAllowed
	}
	requeued, err := q.RequeueConfirmedFailedRequirementPopoCommand(ctx, db.RequeueConfirmedFailedRequirementPopoCommandParams{
		ID: row.ID, WorkspaceID: workspaceID, Attempt: row.Attempt, PayloadHash: row.PayloadHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PopoBridgeCommand{}, ErrRequirementRecoveryNotAllowed
	}
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	return requeued, nil
}
