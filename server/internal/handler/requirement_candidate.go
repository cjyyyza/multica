package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func (h *Handler) requirementPlanTaskActor(w http.ResponseWriter, r *http.Request) (service.RequirementActor, pgtype.UUID, pgtype.UUID, bool) {
	return h.requirementTaskTokenActor(w, r, taskpolicy.ModePlan, "candidate submission requires a plan task actor", true)
}

func (h *Handler) SubmitRequirementCandidate(w http.ResponseWriter, r *http.Request) {
	actor, taskID, requirementID, ok := h.requirementPlanTaskActor(w, r)
	if !ok {
		return
	}
	var input service.SubmitRequirementCandidateRequest
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := h.requirementService().SubmitRequirementCandidate(r.Context(), actor, taskID, requirementID, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}
