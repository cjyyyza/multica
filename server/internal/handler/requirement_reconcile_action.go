package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func (h *Handler) ReconcileRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.ReconcileRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		return svc.ReconcileRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID)
	})
}
