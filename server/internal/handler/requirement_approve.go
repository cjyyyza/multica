package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

type requirementApproveResponse struct {
	Requirement service.RequirementDetail `json:"requirement"`
	Issue       json.RawMessage           `json:"issue"`
}

func (h *Handler) ApproveRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.ApproveRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		buildResponse := func(detail service.RequirementDetail, issue json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(requirementApproveResponse{Requirement: detail, Issue: issue})
		}
		return svc.ApproveRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID, buildResponse)
	})
}
