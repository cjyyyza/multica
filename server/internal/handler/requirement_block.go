package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func (h *Handler) requirementBlockActor(w http.ResponseWriter, r *http.Request) (service.RequirementActor, pgtype.UUID, pgtype.UUID, bool) {
	return h.requirementTaskTokenActor(w, r, taskpolicy.ModeExecute, "requirement blocking requires an execute task actor", false)
}

func (h *Handler) BlockRequirement(w http.ResponseWriter, r *http.Request) {
	actor, taskID, requirementID, ok := h.requirementBlockActor(w, r)
	if !ok {
		return
	}
	var input service.BlockRequirementRequest
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := h.requirementService().BlockRequirement(r.Context(), actor, taskID, requirementID, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}
