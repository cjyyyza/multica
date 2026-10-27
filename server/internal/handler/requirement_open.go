package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementOpenResponse struct {
	Requirement service.RequirementDetail `json:"requirement"`
	Session     ChatSessionResponse       `json:"session"`
	PendingTask *PendingChatTaskResponse  `json:"pendingTask,omitempty"`
}

func (h *Handler) OpenRequirement(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.OpenRequirementRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		buildResponse := func(detail service.RequirementDetail, session db.ChatSession, task *db.AgentTaskQueue) (json.RawMessage, error) {
			var pending *PendingChatTaskResponse
			if task != nil {
				pending = &PendingChatTaskResponse{
					TaskID: uuidToString(task.ID), Status: task.Status,
					CreatedAt: timestampToString(task.CreatedAt), SupportsQueue: true,
				}
			}
			// The session in this response was created by (or is already bound to)
			// this very requirement, so the additive requirement_id projection is
			// known without another read. Write it explicitly: chatSessionToResponse
			// alone would serialize null, whose contract meaning is "definitely no
			// requirement" — the opposite of what this response proves.
			sessionResponse := chatSessionToResponse(session)
			if detail.ID != "" {
				id := detail.ID
				sessionResponse.RequirementID = &id
			}
			return json.Marshal(requirementOpenResponse{Requirement: detail, Session: sessionResponse, PendingTask: pending})
		}
		return svc.OpenRequirement(ctx, actor, requirementID, input, idempotencyKey, requestID, buildResponse)
	})
}
