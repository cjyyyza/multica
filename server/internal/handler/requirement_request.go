package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ReplyRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(ctx context.Context, svc *service.RequirementService, actor service.RequirementActor, id pgtype.UUID, input service.ReplyRequirementRequest, key, requestID string) (service.RequirementActionResult, error) {
		return svc.ReplyRequirement(ctx, actor, id, input, key, requestID)
	})
}

func (h *Handler) SendRequirementMessage(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(ctx context.Context, svc *service.RequirementService, actor service.RequirementActor, id pgtype.UUID, input service.SendRequirementMessageRequest, key, requestID string) (service.RequirementActionResult, error) {
		return svc.SendRequirementMessage(ctx, actor, id, input, key, requestID)
	})
}

func (h *Handler) CreateNativeRequirement(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	var input service.CreateNativeRequirementRequest
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := h.requirementService().CreateNativeRequirement(r.Context(), actor, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}

type requirementListResponse struct {
	Requirements []service.RequirementSummary `json:"requirements"`
	HasMore      bool                         `json:"hasMore"`
	NextOffset   *int                         `json:"nextOffset,omitempty"`
}

func (h *Handler) ListRequirements(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	limit, offset := 25, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 || (key == "limit" && (parsed < 1 || parsed > 100)) || parsed > 2147483646 {
				writeError(w, http.StatusBadRequest, "invalid pagination")
				return
			}
			*target = parsed
		}
	}
	externalRef := strings.TrimSpace(r.URL.Query().Get("externalRef"))
	rows, err := h.Queries.ListRequirementsForParticipant(r.Context(), db.ListRequirementsForParticipantParams{
		WorkspaceID: actor.WorkspaceID, UserID: actor.UserID, ExternalRef: pgtype.Text{String: externalRef, Valid: externalRef != ""},
		PageLimit: int32(limit + 1), PageOffset: int32(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list requirements")
		return
	}
	out := requirementListResponse{Requirements: make([]service.RequirementSummary, 0, limit), HasMore: len(rows) > limit}
	if out.HasMore {
		rows = rows[:limit]
		next := offset + limit
		out.NextOffset = &next
	}
	for _, row := range rows {
		summary, err := service.RequirementSummaryForRow(row)
		if err != nil {
			h.writeRequirementResult(w, r, service.RequirementActionResult{}, err)
			return
		}
		out.Requirements = append(out.Requirements, summary)
	}
	writeJSON(w, http.StatusOK, out)
}
