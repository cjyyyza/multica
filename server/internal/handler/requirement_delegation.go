package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/service"
)

func (h *Handler) CreateRequirementDelegation(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	var input service.CreateRequirementDelegationRequest
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := h.requirementService().CreateRequirementDelegation(r.Context(), actor, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}
