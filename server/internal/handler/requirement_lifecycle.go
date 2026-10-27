package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func (h *Handler) AcceptRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.AcceptRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		return svc.AcceptRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID)
	})
}

func (h *Handler) RejectRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.RejectRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		return svc.RejectRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID)
	})
}

func (h *Handler) CancelRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.CancelRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		return svc.CancelRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID)
	})
}
