package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) completeYixiezuoReconcileOperation(w http.ResponseWriter, r *http.Request, row db.YixiezuoOperation, req yixiezuo.Completion, member db.Member) {
	if req.State != "succeeded" && req.State != "failed" && req.State != "unknown" {
		writeError(w, http.StatusBadRequest, "invalid reconcile completion state")
		return
	}
	if req.TargetState != "succeeded" && req.TargetState != "failed" && req.TargetState != "needs_reconcile" {
		writeError(w, http.StatusBadRequest, "invalid reconcile target state")
		return
	}
	if !row.TargetOperationID.Valid {
		writeError(w, http.StatusConflict, "reconcile target operation is missing")
		return
	}
	ws := row.WorkspaceID
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin reconcile receipt")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	target, err := q.LockYixiezuoOperation(r.Context(), db.LockYixiezuoOperationParams{WorkspaceID: ws, ID: row.TargetOperationID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "reconcile target operation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock reconcile target")
		return
	}
	if target.Kind != "publish" {
		writeError(w, http.StatusConflict, "reconcile target is not a publication")
		return
	}
	targetState := req.TargetState
	if target.State == "succeeded" && targetState != "succeeded" {
		writeError(w, http.StatusConflict, "target publication already succeeded")
		return
	}
	if target.State == "failed" && targetState == "needs_reconcile" {
		targetState = "failed"
	}
	if target.State == "running" && targetState == "failed" {
		targetState = "needs_reconcile"
	}
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(target.Payload, &payload); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid stored publication")
		return
	}
	if targetState == "succeeded" && req.Snapshot == nil {
		writeError(w, http.StatusBadRequest, "source snapshot is required to confirm publication")
		return
	}
	result := target.Result
	if req.Snapshot != nil {
		if err := req.Snapshot.Validate(payload.Source); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Snapshot.Digest = req.Snapshot.Fingerprint()
		result, err = json.Marshal(req.Snapshot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode reconcile snapshot")
			return
		}
	}
	updated := row
	if row.State == "running" {
		updated, err = q.CompleteYixiezuoOperation(r.Context(), db.CompleteYixiezuoOperationParams{
			WorkspaceID: ws, RequestedBy: member.UserID, ID: row.ID, LeaseToken: row.LeaseToken,
			State: req.State, Result: result, Error: req.Error,
		})
		if err != nil {
			writeError(w, http.StatusConflict, "reconcile operation lease expired")
			return
		}
	} else if row.State != req.State {
		writeError(w, http.StatusConflict, "reconcile operation already completed")
		return
	}
	if _, err = q.ReconcileYixiezuoOperationState(r.Context(), db.ReconcileYixiezuoOperationStateParams{
		WorkspaceID: ws, TargetOperationID: target.ID, State: targetState, Result: result, Error: req.Error,
	}); err != nil {
		writeError(w, http.StatusConflict, "target publication changed since readback")
		return
	}
	if targetState == "succeeded" {
		if err := q.RecordYixiezuoPublication(r.Context(), db.RecordYixiezuoPublicationParams{
			WorkspaceID: ws, IssueID: target.IssueID, Snapshot: result, PublishedRevision: payload.Revision,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record publication")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit reconcile receipt")
		return
	}
	h.publishRequirementReconcileUpdate(r, ws, target, member)
	writeJSON(w, http.StatusOK, operationResponse(updated, false))
}

func (h *Handler) publishRequirementReconcileUpdate(r *http.Request, workspaceID pgtype.UUID, target db.YixiezuoOperation, member db.Member) {
	if !target.RequirementID.Valid {
		return
	}
	detail, err := h.requirementService().GetRequirement(r.Context(), service.RequirementActor{WorkspaceID: workspaceID, UserID: member.UserID}, target.RequirementID)
	if err != nil {
		return
	}
	payload := map[string]any{
		"workspaceId": uuidToString(workspaceID), "requirementId": detail.ID, "revision": detail.Revision,
	}
	if detail.ChatSessionID != nil {
		payload["chatSessionId"] = *detail.ChatSessionID
	}
	h.publish(protocol.EventRequirementUpdated, uuidToString(workspaceID), "member", uuidToString(member.UserID), payload)
}
