package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) loadIssueP4Content(w http.ResponseWriter, r *http.Request) (db.Issue, db.P4Changelist, bool) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Issue{}, db.P4Changelist{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "changelistId"), "changelist id")
	if !ok {
		return db.Issue{}, db.P4Changelist{}, false
	}
	if _, linked := h.p4ChangelistForIssue(r.Context(), issue.ID, issue.WorkspaceID, id); !linked {
		writeError(w, http.StatusNotFound, "changelist is not linked to this issue")
		return db.Issue{}, db.P4Changelist{}, false
	}
	row, err := h.Queries.GetP4ChangelistInWorkspace(r.Context(), db.GetP4ChangelistInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "changelist not found")
		return db.Issue{}, db.P4Changelist{}, false
	}
	return issue, row, true
}

func (h *Handler) RequestIssueP4Content(w http.ResponseWriter, r *http.Request) {
	issue, cl, ok := h.loadIssueP4Content(w, r)
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-User-ID"), "user id")
	if !ok {
		return
	}
	var input struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.File) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid content request")
		return
	}
	runtimes, err := h.Queries.ListVisibleAgentRuntimes(r.Context(), db.ListVisibleAgentRuntimesParams{WorkspaceID: issue.WorkspaceID, OwnerID: userID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list Perforce daemons")
		return
	}
	var selected *db.AgentRuntime
	best := -1
	for i := range runtimes {
		rt := &runtimes[i]
		if rt.Status != "online" || !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityP4ContentV1) {
			continue
		}
		rank := 0
		if rt.OwnerID == userID {
			rank++
		}
		if cl.DaemonID != "" && rt.DaemonID.String == cl.DaemonID {
			rank += 2
		}
		if rank > best {
			selected, best = rt, rank
		}
	}
	if selected == nil {
		writeError(w, http.StatusServiceUnavailable, "reading changelists requires an online daemon with p4-content-v1; update and connect a daemon with Perforce access")
		return
	}
	req, err := h.P4ContentStore.Create(r.Context(), P4ContentRequest{
		WorkspaceID: uuidToString(issue.WorkspaceID), IssueID: uuidToString(issue.ID), ChangelistID: uuidToString(cl.ID),
		RuntimeID: uuidToString(selected.ID), UserID: uuidToString(userID), File: input.File,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to request changelist content")
		return
	}
	h.requestDaemonPendingWork(req.RuntimeID, "p4_content")
	writeJSON(w, http.StatusOK, req)
}

func (h *Handler) GetIssueP4Content(w http.ResponseWriter, r *http.Request) {
	issue, cl, ok := h.loadIssueP4Content(w, r)
	if !ok {
		return
	}
	req, err := h.P4ContentStore.Get(r.Context(), chi.URLParam(r, "requestId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read changelist content")
		return
	}
	if req == nil || req.WorkspaceID != uuidToString(issue.WorkspaceID) || req.IssueID != uuidToString(issue.ID) || req.ChangelistID != uuidToString(cl.ID) || req.UserID != r.Header.Get("X-User-ID") {
		writeError(w, http.StatusNotFound, "content request not found")
		return
	}
	if req.Status == "pending" {
		// A second file read can arrive inside the daemon's one-second hint
		// throttle. Polling reissues the hint until the request is claimed.
		h.requestDaemonPendingWork(req.RuntimeID, "p4_content")
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *Handler) daemonP4ContentRequest(w http.ResponseWriter, r *http.Request) (*P4ContentRequest, db.AgentRuntime, bool) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return nil, db.AgentRuntime{}, false
	}
	req, err := h.P4ContentStore.Get(r.Context(), chi.URLParam(r, "requestId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load content request")
		return nil, db.AgentRuntime{}, false
	}
	if req == nil || req.RuntimeID != uuidToString(rt.ID) || req.WorkspaceID != uuidToString(rt.WorkspaceID) {
		writeError(w, http.StatusNotFound, "content request not found")
		return nil, db.AgentRuntime{}, false
	}
	return req, rt, true
}

// GetDaemonP4ContentContext resolves current configured resources at execution
// time. Neither a pasted CL port nor a requested depot path authorizes a server.
func (h *Handler) GetDaemonP4ContentContext(w http.ResponseWriter, r *http.Request) {
	req, rt, ok := h.daemonP4ContentRequest(w, r)
	if !ok {
		return
	}
	ws := rt.WorkspaceID
	clID := parseUUID(req.ChangelistID)
	if _, linked := h.p4ChangelistForIssue(r.Context(), parseUUID(req.IssueID), ws, clID); !linked {
		writeError(w, http.StatusNotFound, "changelist is no longer linked")
		return
	}
	cl, err := h.Queries.GetP4ChangelistInWorkspace(r.Context(), db.GetP4ChangelistInWorkspaceParams{WorkspaceID: ws, ID: clID})
	if err != nil {
		writeError(w, http.StatusNotFound, "changelist not found")
		return
	}
	depots, err := h.p4PollingDepots(r.Context(), ws, []db.ListOpenP4ChangelistsForDaemonRow{{ID: clID}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve Perforce resources")
		return
	}
	if !p4PollResourceAllowed(cl.Port, cl.ServerKey, cl.Client, cl.DaemonID, depots) {
		writeError(w, http.StatusForbidden, "no current Perforce resource authorizes this read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": req.WorkspaceID, "id": cl.ID, "port": cl.Port, "server_key": cl.ServerKey,
		"client": cl.Client, "daemon_id": cl.DaemonID, "p4_user": cl.P4User, "charset": cl.Charset,
		"number": cl.Number, "original_number": cl.OriginalNumber, "p4_depots": depots, "file": req.File,
		// Private working files stay owner-only even on a shared runtime.
		"allow_workspace": req.UserID == uuidToString(rt.OwnerID) && (cl.DaemonID == "" || cl.DaemonID == rt.DaemonID.String),
	})
}

func (h *Handler) ReportP4ContentResult(w http.ResponseWriter, r *http.Request) {
	req, _, ok := h.daemonP4ContentRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Result *protocol.P4ChangelistContent `json:"result"`
		Error  string                        `json:"error"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024*1024)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Result == nil && strings.TrimSpace(body.Error) == "") {
		writeError(w, http.StatusBadRequest, "invalid content result")
		return
	}
	if err := h.P4ContentStore.Complete(r.Context(), req.ID, body.Result, body.Error); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store content result")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
