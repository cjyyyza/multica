package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func (h *Handler) requirementActor(w http.ResponseWriter, r *http.Request) (service.RequirementActor, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return service.RequirementActor{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return service.RequirementActor{}, false
	}
	actorType, _ := h.resolveActor(r, uuidToString(member.UserID), workspaceID)
	if actorType != "member" {
		writeError(w, http.StatusForbidden, "requirement actions require a human workspace member")
		return service.RequirementActor{}, false
	}
	return service.RequirementActor{WorkspaceID: workspaceUUID, UserID: member.UserID}, true
}

func (h *Handler) requirementService() *service.RequirementService {
	svc := service.NewRequirementService(h.Queries, h.TxStarter)
	svc.Tasks = h.TaskService
	svc.Issues = h.IssueService
	svc.FeatureFlags = h.FeatureFlags
	svc.AppURL = h.cfg.AppURL
	if h.PopoRequirementOutbox != nil {
		svc.DeliveryReconciler = h.PopoRequirementOutbox
	}
	svc.RunProjector = func(workspaceID string, task db.AgentTaskQueue) (json.RawMessage, error) {
		return json.Marshal(taskToResponse(task, workspaceID))
	}
	return svc
}

func decodeRequirementBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// handleRequirementMemberAction runs the pipeline shared by human-member
// requirement actions: authorize the member, resolve the routed requirement,
// decode the action input, call the service, and write its result.
func handleRequirementMemberAction[Req any](
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	action func(context.Context, *service.RequirementService, service.RequirementActor, pgtype.UUID, Req, string, string) (service.RequirementActionResult, error),
) {
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	requirementID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requirementId"), "requirement_id")
	if !ok {
		return
	}
	var input Req
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := action(r.Context(), h.requirementService(), actor, requirementID, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}

// requirementTaskTokenActor authorizes a daemon task-token actor for plan and
// execute task requirement routes and returns its agent actor, task ID, and
// routed requirement ID. forbiddenMessage names the calling action in the 403
// response; requireChatSession enforces the plan task's chat scope.
func (h *Handler) requirementTaskTokenActor(
	w http.ResponseWriter, r *http.Request, expectedMode, forbiddenMessage string, requireChatSession bool,
) (service.RequirementActor, pgtype.UUID, pgtype.UUID, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	requirementID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requirementId"), "requirement_id")
	if !ok {
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	if r.Header.Get("X-Actor-Source") != "task_token" || r.Header.Get(taskpolicy.HeaderTaskMode) != expectedMode {
		writeError(w, http.StatusForbidden, forbiddenMessage)
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Agent-ID"), "agent_id")
	if !ok {
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	if scoped := r.Header.Get(taskpolicy.HeaderRequirementID); scoped == "" || parseUUID(scoped) != requirementID {
		writeError(w, http.StatusForbidden, "task requirement scope does not match the route")
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	if requireChatSession && r.Header.Get(taskpolicy.HeaderChatSessionID) == "" {
		writeError(w, http.StatusForbidden, "task chat scope is required")
		return service.RequirementActor{}, pgtype.UUID{}, pgtype.UUID{}, false
	}
	return service.RequirementActor{WorkspaceID: workspaceUUID, UserID: agentID}, taskID, requirementID, true
}

func (h *Handler) CreateRequirement(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	var input service.CreateRequirementRequest
	if !decodeRequirementBody(w, r, &input) {
		return
	}
	result, err := h.requirementService().CreateRequirement(r.Context(), actor, input, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
	h.writeRequirementResult(w, r, result, err)
}

func (h *Handler) GetRequirementByID(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		mode := r.Header.Get(taskpolicy.HeaderTaskMode)
		if mode != taskpolicy.ModePlan && mode != taskpolicy.ModeExecute {
			writeError(w, http.StatusForbidden, "requirement read requires a scoped plan or execute task")
			return
		}
		actor, taskID, requirementID, ok := h.requirementTaskTokenActor(w, r, mode, "requirement read requires a task actor", mode == taskpolicy.ModePlan)
		if !ok {
			return
		}
		detail, err := h.requirementService().GetRequirementForTask(r.Context(), actor, taskID, requirementID, mode)
		h.writeRequirementResult(w, r, service.RequirementActionResult{Status: http.StatusOK, Body: mustMarshalRequirementDetail(detail)}, err)
		return
	}
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	requirementID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requirementId"), "requirement_id")
	if !ok {
		return
	}
	detail, err := h.requirementService().GetRequirement(r.Context(), actor, requirementID)
	if err == nil && !requireRequirementCandidateClient(w, r, detail) {
		return
	}
	h.writeRequirementResult(w, r, service.RequirementActionResult{Status: http.StatusOK, Body: mustMarshalRequirementDetail(detail)}, err)
}

func requireRequirementCandidateClient(w http.ResponseWriter, r *http.Request, detail service.RequirementDetail) bool {
	if detail.LatestCandidate == nil || detail.LatestCandidate.CreatedByTaskID != nil ||
		requestHasClientCapability(r, protocol.AppCapabilityRequirementCandidateProvenanceV1) {
		return true
	}
	writeJSON(w, http.StatusUpgradeRequired, map[string]any{
		"code":                "requirement_client_upgrade_required",
		"error":               "This requirement has an externally submitted candidate. Update Multica or open it in the current web app to review it.",
		"required_capability": protocol.AppCapabilityRequirementCandidateProvenanceV1,
	})
	return false
}

func (h *Handler) GetRequirementByChat(w http.ResponseWriter, r *http.Request) {
	if _, scopedToChat := r.URL.Query()["chat_session_id"]; !scopedToChat {
		h.ListRequirements(w, r)
		return
	}
	actor, ok := h.requirementActor(w, r)
	if !ok {
		return
	}
	chatSessionID, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("chat_session_id"), "chat_session_id")
	if !ok {
		return
	}
	detail, err := h.requirementService().GetRequirementByChat(r.Context(), actor, chatSessionID)
	if err == nil && !requireRequirementCandidateClient(w, r, detail) {
		return
	}
	h.writeRequirementResult(w, r, service.RequirementActionResult{Status: http.StatusOK, Body: mustMarshalRequirementDetail(detail)}, err)
}

func (h *Handler) UpdateRequirementParticipants(w http.ResponseWriter, r *http.Request) {
	handleRequirementMemberAction(h, w, r, func(
		ctx context.Context, svc *service.RequirementService, actor service.RequirementActor,
		requirementID pgtype.UUID, input service.UpdateRequirementParticipantsRequest, idempotencyKey, requestID string,
	) (service.RequirementActionResult, error) {
		return svc.UpdateRequirementParticipants(ctx, actor, requirementID, input, idempotencyKey, requestID)
	})
}

func mustMarshalRequirementDetail(detail service.RequirementDetail) json.RawMessage {
	body, _ := json.Marshal(detail)
	return body
}

func (h *Handler) writeRequirementResult(w http.ResponseWriter, r *http.Request, result service.RequirementActionResult, err error) {
	if err != nil {
		writeRequirementError(w, err)
		return
	}
	for _, update := range result.IssueUpdates {
		response := issueToResponse(update.Issue, h.getIssuePrefix(r.Context(), update.Issue.WorkspaceID))
		h.fillStatusCategory(r.Context(), update.Issue.WorkspaceID, &response)
		h.publish(protocol.EventIssueUpdated, result.WorkspaceID, result.ActorType, result.ActorID, map[string]any{
			"issue": response, "status_changed": update.PreviousStatus != update.Issue.Status, "prev_status": update.PreviousStatus,
		})
	}
	for _, event := range result.Comments {
		h.publishEvidenceComment(r.Context(), event.Issue, event.Comment, event.IssueRevision)
	}
	for _, message := range result.ChatMessages {
		h.publishChat(protocol.EventChatMessage, result.WorkspaceID, result.ActorType, result.ActorID, uuidToString(message.ChatSessionID), protocol.ChatMessagePayload{
			ChatSessionID: uuidToString(message.ChatSessionID), MessageID: uuidToString(message.ID), Role: message.Role, Content: message.Content, TaskID: uuidToString(message.TaskID), CreatedAt: timestampToString(message.CreatedAt),
		})
	}
	if len(result.CancelledTasks) > 0 {
		h.TaskService.FinalizeCancelledTasks(r.Context(), result.CancelledTasks)
	}
	for _, task := range result.FinalizeTasks {
		h.TaskService.FinalizeChatTaskEnqueue(r.Context(), task)
	}
	for _, task := range result.PromoteTasks {
		if err := h.TaskService.PromoteDeferredChannelIssueTask(r.Context(), task.ID); err != nil {
			slog.Warn("requirement initial task promote failed; recovery must retry the existing task", "task_id", uuidToString(task.ID), "error", err)
		}
	}
	if result.PostCommit != nil {
		result.PostCommit()
	}
	if result.EventType != "" {
		pointer := result.Realtime
		if pointer == nil || pointer.ID == "" || pointer.WorkspaceID != result.WorkspaceID || pointer.Revision <= 0 {
			slog.Error("requirement realtime event has no valid readback pointer", "workspace_id", result.WorkspaceID)
		} else {
			payload := map[string]any{
				"workspaceId": result.WorkspaceID, "requirementId": pointer.ID, "revision": pointer.Revision,
			}
			if pointer.ChatSessionID != nil {
				payload["chatSessionId"] = *pointer.ChatSessionID
			}
			h.publish(result.EventType, result.WorkspaceID, result.ActorType, result.ActorID, payload)
		}
	}
	writeJSON(w, result.Status, result.Body)
}

func writeRequirementError(w http.ResponseWriter, err error) {
	if writeRequirementChatExecutionError(w, err) {
		return
	}
	var requirementErr *service.RequirementError
	if !errors.As(err, &requirementErr) {
		writeError(w, http.StatusInternalServerError, "failed to process requirement")
		return
	}
	body := map[string]any{"error": requirementErr.Message, "code": requirementErr.Code}
	if requirementErr.ExistingRequirementID != "" {
		body["existing_requirement_id"] = requirementErr.ExistingRequirementID
	}
	if requirementErr.ResourceType != "" {
		body["resource_type"] = requirementErr.ResourceType
	}
	if requirementErr.ResourceID != "" {
		body["resource_id"] = requirementErr.ResourceID
	}
	if requirementErr.ExpectedRevision != nil {
		body["expected_revision"] = *requirementErr.ExpectedRevision
	}
	if requirementErr.ActualRevision != nil {
		body["actual_revision"] = *requirementErr.ActualRevision
	}
	writeJSON(w, requirementErr.Status, body)
}
