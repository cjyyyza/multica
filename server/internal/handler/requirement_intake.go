package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementDraftRequest struct {
	TemplateURL string `json:"template_url"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	AgentID     string `json:"agent_id"`
}

func (h *Handler) queueRequirementDraft(ctx context.Context, ws, actor pgtype.UUID, input requirementDraftRequest, key string, scope *yixiezuo.ChannelScope) (db.YixiezuoOperation, error) {
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return db.YixiezuoOperation{}, sourceError(400, "a durable Idempotency-Key of at most 200 bytes is required")
	}
	if !featureflags.PMMCPWriteEnabled(ctx, h.FeatureFlags) {
		return db.YixiezuoOperation{}, sourceError(403, "pmmcp_write is disabled")
	}
	template, err := yixiezuo.ParseSource(input.TemplateURL)
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(400, err.Error())
	}
	agentID, err := util.ParseUUID(input.AgentID)
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(400, "agent_id must be a UUID")
	}
	if err := h.requirementService().ValidateDraftAgent(ctx, service.RequirementActor{WorkspaceID: ws, UserID: actor}, agentID); err != nil {
		var action *service.RequirementError
		if errors.As(err, &action) {
			return db.YixiezuoOperation{}, sourceError(action.Status, action.Message)
		}
		return db.YixiezuoOperation{}, err
	}
	draft := &yixiezuo.DraftInput{Template: template, Title: strings.TrimSpace(input.Title), Body: strings.TrimSpace(input.Body), AgentID: uuidToString(agentID)}
	if err := draft.Validate(); err != nil {
		return db.YixiezuoOperation{}, sourceError(400, err.Error())
	}
	payload := yixiezuo.OperationPayload{Draft: draft, Channel: scope}
	raw, _ := json.Marshal(payload)
	requestHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	row, err := h.Queries.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, Kind: "draft", Payload: raw, RequestKey: sourceRequestKey("draft:" + key), RequestHash: pgtype.Text{String: requestHash, Valid: true}})
	if err != nil {
		return row, err
	}
	var saved yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &saved) != nil || row.Kind != "draft" || saved.Draft == nil || *saved.Draft != *draft || !sourceScopeMatches(saved.Channel, scope) {
		return row, sourceError(409, "this request already identifies a different draft")
	}
	return h.settleRequirementDraft(ctx, row)
}

func (h *Handler) CreateRequirementDraft(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	var input requirementDraftRequest
	if !decodeYixiezuoBody(w, r, &input) {
		return
	}
	row, err := h.queueRequirementDraft(r.Context(), ws, member.UserID, input, r.Header.Get("Idempotency-Key"), nil)
	if err != nil {
		writeSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, operationResponseWithFlags(r.Context(), row, false, h.FeatureFlags))
}

func (h *Handler) settleRequirementDraft(ctx context.Context, row db.YixiezuoOperation) (db.YixiezuoOperation, error) {
	if row.Kind != "draft" || row.State != "succeeded" || row.RequirementID.Valid {
		return row, nil
	}
	var payload yixiezuo.OperationPayload
	var snapshot yixiezuo.Snapshot
	if json.Unmarshal(row.Payload, &payload) != nil || payload.Draft == nil || json.Unmarshal(row.Result, &snapshot) != nil || snapshot.Source != payload.Source {
		return row, fmt.Errorf("invalid completed draft material")
	}
	if err := yixiezuo.ValidateDraftSnapshot(uuidToString(row.ID), *payload.Draft, snapshot); err != nil {
		return row, err
	}
	operationID := uuidToString(row.ID)
	result, err := h.requirementService().CreateRequirement(ctx, service.RequirementActor{WorkspaceID: row.WorkspaceID, UserID: row.RequestedBy}, service.CreateRequirementRequest{
		SourceHost: snapshot.Source.Host, ExternalID: snapshot.Source.ID, SourceUrl: snapshot.Source.URL,
		SourceSnapshot: row.Result, SourceDigest: snapshot.Digest, SourceLockVersion: snapshot.LockVersion,
		AgentID: payload.Draft.AgentID, IntakeOperationID: &operationID,
	}, "draft:"+operationID, "draft:"+operationID)
	if err != nil {
		return row, err
	}
	var detail service.RequirementDetail
	if err := json.Unmarshal(result.Body, &detail); err != nil {
		return row, err
	}
	requirementID, err := util.ParseUUID(detail.ID)
	if err != nil {
		return row, err
	}
	bound, err := h.Queries.BindYixiezuoDraftRequirement(ctx, db.BindYixiezuoDraftRequirementParams{WorkspaceID: row.WorkspaceID, RequestedBy: row.RequestedBy, OperationID: row.ID, RequirementID: requirementID})
	if err == nil && result.EventType != "" && result.Realtime != nil {
		h.publish(result.EventType, uuidToString(row.WorkspaceID), "member", uuidToString(row.RequestedBy), map[string]any{"workspaceId": uuidToString(row.WorkspaceID), "requirementId": detail.ID, "revision": result.Realtime.Revision})
	}
	return bound, err
}

func (h *Handler) settleCompletedRequirementDraft(ctx context.Context, row db.YixiezuoOperation) (db.YixiezuoOperation, error) {
	if row.Kind == "draft" {
		h.notifyRequirementDraftOutcome(ctx, row)
		return h.settleRequirementDraft(ctx, row)
	}
	var payload yixiezuo.OperationPayload
	if row.Kind != "preview" || row.State != "succeeded" || json.Unmarshal(row.Payload, &payload) != nil || payload.DraftOperationID == "" {
		return row, nil
	}
	targetID, err := util.ParseUUID(payload.DraftOperationID)
	if err != nil {
		return row, err
	}
	target, err := h.Queries.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{WorkspaceID: row.WorkspaceID, RequestedBy: row.RequestedBy, ID: targetID})
	if err == nil {
		_, err = h.settleRequirementDraft(ctx, target)
	}
	return row, err
}

func (h *Handler) recoverRequirementDrafts(ctx context.Context, ws, actor pgtype.UUID) error {
	rows, err := h.Queries.ListUnsettledYixiezuoDrafts(ctx, db.ListUnsettledYixiezuoDraftsParams{WorkspaceID: ws, RequestedBy: actor})
	if err != nil {
		return err
	}
	for _, row := range rows {
		// A removed member or disabled feature must not stop unrelated bridge reads.
		_, _ = h.settleRequirementDraft(ctx, row)
	}
	return nil
}

func (h *Handler) validateDraftChannel(ctx context.Context, ws, actor, agentID pgtype.UUID, payload yixiezuo.OperationPayload) error {
	if payload.Channel == nil {
		return nil
	}
	id, err := util.ParseUUID(payload.Channel.InstallationID)
	if err != nil || strings.TrimSpace(payload.Channel.ChatID) == "" || (payload.Channel.ChatType != "group" && payload.Channel.ChatType != "p2p") {
		return sourceError(409, "the source draft channel is invalid")
	}
	installation, err := h.Queries.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{ID: id, WorkspaceID: ws, ChannelType: "popo"})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (installation.Status != "active" || installation.AgentID != agentID)) {
		return sourceError(409, "the source robot changed before draft creation")
	}
	if err != nil {
		return err
	}
	_, err = h.Queries.ResolveRequirementRecipientBinding(ctx, db.ResolveRequirementRecipientBindingParams{WorkspaceID: ws, RecipientUserID: actor, AgentID: agentID, InstallationID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return sourceError(409, "the source robot or member binding changed before draft creation")
	}
	return err
}

func (h *Handler) queueRequirementDraftReconcile(ctx context.Context, ws, actor pgtype.UUID, draftID pgtype.UUID, sourceURL, key string, scope *yixiezuo.ChannelScope) (db.YixiezuoOperation, error) {
	if err := h.expireYixiezuoOperations(ctx, ws); err != nil {
		return db.YixiezuoOperation{}, err
	}
	target, err := h.Queries.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, ID: draftID})
	if err != nil || target.Kind != "draft" {
		return target, sourceError(404, "draft operation not found for this member")
	}
	var payload yixiezuo.OperationPayload
	if json.Unmarshal(target.Payload, &payload) != nil || payload.Draft == nil || !sourceScopeMatches(payload.Channel, scope) {
		return target, sourceError(403, "use the same member and originating channel as the draft")
	}
	if saved, exists, loadErr := existingSourceRequest(ctx, h.Queries, ws, actor, key); loadErr != nil {
		return target, loadErr
	} else if exists {
		var savedPayload yixiezuo.OperationPayload
		source, sourceErr := yixiezuo.ParseSource(sourceURL)
		if json.Unmarshal(saved.Payload, &savedPayload) != nil || saved.Kind != "preview" || savedPayload.DraftOperationID != uuidToString(target.ID) || savedPayload.Source != source || sourceErr != nil || !sourceScopeMatches(savedPayload.Channel, scope) {
			return saved, sourceError(409, "this request identifies another draft readback")
		}
		return saved, nil
	}
	if target.State != "unknown" && target.State != "needs_reconcile" {
		return target, sourceError(409, "only an unconfirmed draft can be reconciled")
	}
	source, err := yixiezuo.ParseSource(sourceURL)
	if err != nil || source.Host != payload.Draft.Template.Host || source.ID == payload.Draft.Template.ID {
		return target, sourceError(400, "select the newly created issue on the draft's source host")
	}
	return h.queueYixiezuoPreview(ctx, ws, actor, source.URL, key, scope, yixiezuo.OperationPayload{DraftOperationID: uuidToString(target.ID)})
}

func (h *Handler) notifyRequirementDraftOutcome(ctx context.Context, row db.YixiezuoOperation) {
	if row.Kind != "draft" || (row.State != "unknown" && row.State != "failed") || h.PopoBridge == nil {
		return
	}
	var payload yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &payload) != nil || payload.Channel == nil {
		return
	}
	installationID, err := util.ParseUUID(payload.Channel.InstallationID)
	if err != nil {
		return
	}
	_ = h.PopoBridge.Enqueue(ctx, popo.OutboundItem{WorkspaceID: row.WorkspaceID, InstallationID: installationID,
		ChatID: payload.Channel.ChatID, ChatType: payload.Channel.ChatType, Content: h.requirementDraftReply(ctx, row),
		SourceKey: "requirement-draft:" + uuidToString(row.ID) + ":" + row.State, OutboundKind: "issue_ack",
	})
}

func (h *Handler) ReconcileRequirementDraft(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "operationId"), "operation_id")
	if !ok {
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if !decodeYixiezuoBody(w, r, &input) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	row, err := h.queueRequirementDraftReconcile(r.Context(), ws, member.UserID, id, input.URL, "draft-reconcile:"+key, nil)
	if err != nil {
		writeSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, operationResponse(row, false))
}

func resolveRequirementDraftInTx(ctx context.Context, q *db.Queries, preview db.YixiezuoOperation, payload yixiezuo.OperationPayload, snapshot *yixiezuo.Snapshot) error {
	if payload.DraftOperationID == "" || preview.Kind != "preview" {
		return nil
	}
	id, err := util.ParseUUID(payload.DraftOperationID)
	if err != nil {
		return err
	}
	target, err := q.LockYixiezuoOperation(ctx, db.LockYixiezuoOperationParams{WorkspaceID: preview.WorkspaceID, ID: id})
	var intent yixiezuo.OperationPayload
	if err != nil || target.Kind != "draft" || target.RequestedBy != preview.RequestedBy || json.Unmarshal(target.Payload, &intent) != nil || intent.Draft == nil || !sourceScopeMatches(intent.Channel, payload.Channel) {
		return sourceError(409, "draft reconcile target is unavailable")
	}
	if err := yixiezuo.ValidateDraftSnapshot(uuidToString(target.ID), *intent.Draft, *snapshot); err != nil {
		return sourceError(409, err.Error())
	}
	if target.State == "succeeded" && intent.Source == snapshot.Source {
		return nil
	}
	raw, _ := json.Marshal(snapshot)
	_, err = q.ResolveYixiezuoDraft(ctx, db.ResolveYixiezuoDraftParams{WorkspaceID: preview.WorkspaceID, RequestedBy: preview.RequestedBy, OperationID: target.ID, Snapshot: raw})
	if errors.Is(err, pgx.ErrNoRows) {
		return sourceError(409, "draft state changed; reload it before reconciling")
	}
	return err
}

func (h *Handler) requirementDraftReply(ctx context.Context, row db.YixiezuoOperation) string {
	if row.RequirementID.Valid {
		if ws, err := h.Queries.GetWorkspace(ctx, row.WorkspaceID); err == nil {
			return "请在 Multica Chat 中继续澄清和批准：\n" + strings.TrimRight(h.cfg.AppURL, "/") + "/" + url.PathEscape(ws.Slug) + "/chat?requirement=" + url.QueryEscape(uuidToString(row.RequirementID))
		}
	}
	if row.State == "unknown" || row.State == "needs_reconcile" {
		return "草稿创建结果尚未确认，请勿重复新建。请在易协作查找 " + yixiezuo.DraftMarker(uuidToString(row.ID)) + "，找到后发送：\n/requirement resolve " + uuidToString(row.ID) + " <新单据链接>"
	}
	if row.State == "failed" {
		return "草稿尚未创建：" + row.Error
	}
	return "正在创建待澄清草稿，完成后会发送 Multica Chat 链接。\n操作：" + uuidToString(row.ID)
}
