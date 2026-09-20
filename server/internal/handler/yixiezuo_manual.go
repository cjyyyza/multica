package handler

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strings"
)

func (h *Handler) yixiezuoWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, db.Member, bool) {
	wsID := chi.URLParam(r, "id")
	ws, ok := parseUUIDOrBadRequest(w, wsID, "workspace_id")
	if !ok {
		return ws, db.Member{}, false
	}
	member, ok := h.workspaceMember(w, r, wsID)
	if !ok {
		return ws, member, false
	}
	actor, _ := h.resolveActor(r, uuidToString(member.UserID), wsID)
	if actor != "member" {
		writeError(w, http.StatusForbidden, "易协作 imports and publications require a member's explicit action")
		return ws, member, false
	}
	return ws, member, true
}

func decodeYixiezuoBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func operationResponse(row db.YixiezuoOperation, claim bool) yixiezuo.Operation {
	var payload yixiezuo.OperationPayload
	var snapshot *yixiezuo.Snapshot
	_ = json.Unmarshal(row.Payload, &payload)
	if row.State == "succeeded" || row.State == "conflict" {
		_ = json.Unmarshal(row.Result, &snapshot)
	}
	resp := yixiezuo.Operation{ID: uuidToString(row.ID), Kind: row.Kind, State: row.State, Payload: payload, Snapshot: snapshot, Error: row.Error}
	if claim {
		resp.LeaseToken = uuidToString(row.LeaseToken)
	}
	return resp
}

func (h *Handler) PreviewYixiezuoIssue(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !decodeYixiezuoBody(w, r, &req) {
		return
	}
	source, err := yixiezuo.ParseSource(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload, _ := json.Marshal(yixiezuo.OperationPayload{Source: source})
	row, err := h.Queries.CreateYixiezuoOperation(r.Context(), db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, Kind: "preview", Payload: payload})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to queue source preview")
		return
	}
	writeJSON(w, http.StatusCreated, operationResponse(row, false))
}

func (h *Handler) GetYixiezuoOperation(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "operationId"), "operation_id")
	if !ok {
		return
	}
	if err := h.Queries.ExpireYixiezuoOperations(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check operation expiry")
		return
	}
	row, err := h.Queries.GetYixiezuoOperation(r.Context(), db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, ID: id})
	if err != nil {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	writeJSON(w, http.StatusOK, operationResponse(row, false))
}

func (h *Handler) ClaimYixiezuoOperation(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	if err := h.Queries.ExpireYixiezuoOperations(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check operation expiry")
		return
	}
	row, err := h.Queries.ClaimYixiezuoOperation(r.Context(), db.ClaimYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"operation": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim operation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": operationResponse(row, true)})
}

func (h *Handler) CompleteYixiezuoOperation(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "operationId"), "operation_id")
	if !ok {
		return
	}
	var req yixiezuo.Completion
	if !decodeYixiezuoBody(w, r, &req) {
		return
	}
	lease, ok := parseUUIDOrBadRequest(w, req.LeaseToken, "lease_token")
	if !ok {
		return
	}
	if req.State != "succeeded" && req.State != "failed" && req.State != "conflict" && req.State != "unknown" {
		writeError(w, http.StatusBadRequest, "invalid completion state")
		return
	}
	row, err := h.Queries.GetYixiezuoOperation(r.Context(), db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, ID: id})
	if err != nil {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	if row.LeaseToken != lease {
		writeError(w, http.StatusConflict, "stale operation lease")
		return
	}
	// Receipt retries never repeat the remote mutation or change its outcome.
	if row.State != "running" {
		if row.State == req.State {
			writeJSON(w, http.StatusOK, operationResponse(row, false))
			return
		}
		writeError(w, http.StatusConflict, "operation already completed")
		return
	}
	var payload yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &payload) != nil {
		writeError(w, http.StatusInternalServerError, "invalid stored operation")
		return
	}
	if req.State == "succeeded" || req.State == "conflict" {
		if req.Snapshot == nil {
			writeError(w, http.StatusBadRequest, "source snapshot is required")
			return
		}
		if err := req.Snapshot.Validate(payload.Source); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Snapshot.Digest = req.Snapshot.Fingerprint()
	}
	result, _ := json.Marshal(req.Snapshot)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin receipt")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	updated, err := q.CompleteYixiezuoOperation(r.Context(), db.CompleteYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, ID: id, LeaseToken: lease, State: req.State, Result: result, Error: req.Error})
	if err != nil {
		writeError(w, http.StatusConflict, "operation lease expired; refresh the source before retrying")
		return
	}
	if row.IssueID.Valid && req.State == "succeeded" {
		if row.Kind == "publish" {
			err = q.RecordYixiezuoPublication(r.Context(), db.RecordYixiezuoPublicationParams{WorkspaceID: ws, IssueID: row.IssueID, Snapshot: result, PublishedRevision: payload.Revision})
		} else {
			err = q.UpdateYixiezuoImportSnapshot(r.Context(), db.UpdateYixiezuoImportSnapshotParams{WorkspaceID: ws, IssueID: row.IssueID, Snapshot: result})
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record source receipt")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit receipt")
		return
	}
	writeJSON(w, http.StatusOK, operationResponse(updated, false))
}

func (h *Handler) ImportYixiezuoIssue(w http.ResponseWriter, r *http.Request) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	var req struct {
		OperationID string  `json:"operation_id"`
		ProjectID   *string `json:"project_id"`
	}
	if !decodeYixiezuoBody(w, r, &req) {
		return
	}
	opID, ok := parseUUIDOrBadRequest(w, req.OperationID, "operation_id")
	if !ok {
		return
	}
	row, err := h.Queries.GetYixiezuoOperation(r.Context(), db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, ID: opID})
	if err != nil || row.Kind != "preview" || row.State != "succeeded" {
		writeError(w, http.StatusConflict, "read and review a source issue before importing")
		return
	}
	var snapshot yixiezuo.Snapshot
	if json.Unmarshal(row.Result, &snapshot) != nil {
		writeError(w, http.StatusInternalServerError, "invalid source snapshot")
		return
	}
	project := pgtype.UUID{}
	if req.ProjectID != nil && *req.ProjectID != "" {
		project, ok = parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
	}
	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{WorkspaceID: ws, Title: snapshot.Title, Description: pgtype.Text{String: snapshot.Markdown(), Valid: true}, Status: "todo", Priority: yixiezuo.MapIncomingPriority(snapshot.Priority, "medium"), CreatorType: "member", CreatorID: member.UserID, ProjectID: project, AllowDuplicate: true, Yixiezuo: &snapshot}, service.IssueCreateOpts{ActorID: uuidToString(member.UserID), Platform: "yixiezuo", BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
		resp := issueToResponse(issue, h.getIssuePrefix(r.Context(), ws))
		h.fillStatusCategory(r.Context(), ws, &resp)
		return map[string]any{"issue": resp}
	}})
	existing := errors.Is(err, service.ErrYixiezuoAlreadyImported)
	if existing {
		res.Issue = *res.DuplicateIssue
	} else if err != nil {
		if errors.Is(err, service.ErrProjectNotFound) {
			writeError(w, http.StatusBadRequest, "project not found in this workspace")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to import source issue")
		}
		return
	}
	resp := issueToResponse(res.Issue, h.getIssuePrefix(r.Context(), ws))
	h.fillStatusCategory(r.Context(), ws, &resp)
	writeJSON(w, http.StatusOK, map[string]any{"issue": resp, "existing": existing})
}

func (h *Handler) GetYixiezuoImport(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	if err := h.Queries.ExpireYixiezuoOperations(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check operation expiry")
		return
	}
	link, err := h.Queries.GetYixiezuoImportByIssue(r.Context(), db.GetYixiezuoImportByIssueParams{WorkspaceID: ws, IssueID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"import": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load import")
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{WorkspaceID: ws, ID: id})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	var snapshot yixiezuo.Snapshot
	_ = json.Unmarshal(link.Snapshot, &snapshot)
	state := "imported"
	if link.PublishedRevision > 0 {
		state = "published"
		if link.PublishedRevision != issue.Revision {
			state = "needs_review"
		}
	}
	var operation *yixiezuo.Operation
	if latest, err := h.Queries.LatestYixiezuoOperation(r.Context(), db.LatestYixiezuoOperationParams{WorkspaceID: ws, IssueID: id}); err == nil {
		resp := operationResponse(latest, false)
		operation = &resp
		if latest.State != "succeeded" {
			state = latest.State
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"import": map[string]any{"issue_id": uuidToString(id), "snapshot": snapshot, "state": state, "operation": operation, "published_revision": link.PublishedRevision, "published_at": timestampToPtr(link.PublishedAt), "revision": issue.Revision}})
}

func (h *Handler) ListYixiezuoImportStates(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	if err := h.Queries.ExpireYixiezuoOperations(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check operation expiry")
		return
	}
	rows, err := h.Queries.ListYixiezuoImportStates(r.Context(), ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list source states")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) RefreshYixiezuoImport(w http.ResponseWriter, r *http.Request) {
	h.queueYixiezuoIssueOperation(w, r, "refresh")
}
func (h *Handler) PublishYixiezuoResult(w http.ResponseWriter, r *http.Request) {
	h.queueYixiezuoIssueOperation(w, r, "publish")
}

func (h *Handler) queueYixiezuoIssueOperation(w http.ResponseWriter, r *http.Request, kind string) {
	ws, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	var req struct {
		Summary      string `json:"summary"`
		StatusName   string `json:"status_name"`
		Revision     int64  `json:"revision"`
		Confirmed    bool   `json:"confirmed"`
		SourceDigest string `json:"source_digest"`
	}
	if !decodeYixiezuoBody(w, r, &req) {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin operation")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if err := q.ExpireYixiezuoOperations(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to expire stale operations")
		return
	}
	// Serialize publications and refreshes across members and browser tabs.
	if err = q.LockYixiezuoImportSource(r.Context(), uuidToString(ws)+"/issue/"+uuidToString(id)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock import")
		return
	}
	link, err := q.GetYixiezuoImportByIssue(r.Context(), db.GetYixiezuoImportByIssueParams{WorkspaceID: ws, IssueID: id})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue is not imported from 易协作")
		return
	}
	issue, err := q.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{WorkspaceID: ws, ID: id})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	var snapshot yixiezuo.Snapshot
	_ = json.Unmarshal(link.Snapshot, &snapshot)
	if latest, err := q.LatestYixiezuoOperation(r.Context(), db.LatestYixiezuoOperationParams{WorkspaceID: ws, IssueID: id}); err == nil {
		if latest.State == "pending" || latest.State == "running" {
			writeError(w, http.StatusConflict, "an operation is already pending for this issue")
			return
		}
		if kind == "publish" && (latest.State == "unknown" || latest.State == "conflict") {
			writeError(w, http.StatusConflict, "refresh and review the remote source before publishing again")
			return
		}
	}
	if kind == "publish" {
		if req.SourceDigest != snapshot.Digest || req.SourceDigest == "" {
			writeError(w, http.StatusConflict, "the source preview changed; review it before publishing")
			return
		}
		if !req.Confirmed || strings.TrimSpace(req.Summary) == "" || len(req.Summary) > 20000 {
			writeError(w, http.StatusBadRequest, "review and confirm a result summary before publishing")
			return
		}
		if req.Revision != issue.Revision {
			writeError(w, http.StatusConflict, "the Multica issue changed; review its latest revision before publishing")
			return
		}
		if req.StatusName != "" {
			found := false
			for _, status := range snapshot.Statuses {
				if status.Name == req.StatusName {
					found = true
					break
				}
			}
			if !found {
				writeError(w, http.StatusBadRequest, "select a status from the source workflow")
				return
			}
		}
	}
	payload, _ := json.Marshal(yixiezuo.OperationPayload{Source: snapshot.Source, ExpectedDigest: snapshot.Digest, Revision: issue.Revision, StatusName: req.StatusName, Summary: strings.TrimSpace(req.Summary)})
	row, err := q.CreateYixiezuoOperation(r.Context(), db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: member.UserID, Kind: kind, IssueID: id, Payload: payload})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to queue operation")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit operation")
		return
	}
	writeJSON(w, http.StatusCreated, operationResponse(row, false))
}
