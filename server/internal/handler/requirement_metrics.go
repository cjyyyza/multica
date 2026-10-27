package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func requirementSyncMetricFields(metrics db.GetRequirementReconcileMetricsRow) map[string]any {
	return map[string]any{
		"requirement_operations": map[string]any{
			"pending": metrics.OperationPending, "unknown": metrics.OperationUnknown, "needs_reconcile": metrics.OperationNeedsReconcile,
			"oldest_pending_seconds": metrics.OperationOldestPendingSeconds, "oldest_unknown_seconds": metrics.OperationOldestUnknownSeconds,
			"oldest_needs_reconcile_seconds": metrics.OperationOldestNeedsReconcileSeconds,
		},
		"requirement_deliveries": map[string]any{
			"pending": metrics.DeliveryPending, "accepted": metrics.DeliveryAccepted,
			"unknown": metrics.DeliveryUnknown, "needs_reconcile": metrics.DeliveryNeedsReconcile,
			"stale":                  metrics.DeliveryStale,
			"oldest_pending_seconds": metrics.DeliveryOldestPendingSeconds, "oldest_accepted_seconds": metrics.DeliveryOldestAcceptedSeconds,
			"oldest_unknown_seconds":         metrics.DeliveryOldestUnknownSeconds,
			"oldest_needs_reconcile_seconds": metrics.DeliveryOldestNeedsReconcileSeconds,
		},
	}
}

func (h *Handler) requirementSyncMetrics(ctx context.Context, workspaceID pgtype.UUID) (map[string]any, error) {
	metrics, err := h.Queries.GetRequirementReconcileMetrics(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return requirementSyncMetricFields(metrics), nil
}

func mergeRequirementSyncMetrics(target map[string]any, metrics map[string]any) {
	for key, value := range metrics {
		target[key] = value
	}
}
