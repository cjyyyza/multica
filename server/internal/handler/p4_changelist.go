package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Perforce changelists as issue-visible objects.
//
// A Swarm review is not a pull request: nobody merges it, and a changelist can
// outlive several reviews. So the changelist is the object an issue carries,
// the review hangs off it, and the daemon's poll — we have no admin rights for
// a server trigger — is what moves its status.
//
// Identity is (workspace_id, server_key, original_number), not the number a
// person reads today: submitting renumbers a changelist, and both numbers have
// to keep resolving to the same row.

const (
	p4ChangelistStatusPending   = "pending"
	p4ChangelistStatusShelved   = "shelved"
	p4ChangelistStatusSubmitted = "submitted"
	p4ChangelistStatusDeleted   = "deleted"

	p4ChangelistLinkTask   = "task"
	p4ChangelistLinkManual = "manual"
)

func validP4ChangelistStatus(status string) bool {
	switch status {
	case p4ChangelistStatusPending, p4ChangelistStatusShelved, p4ChangelistStatusSubmitted, p4ChangelistStatusDeleted:
		return true
	}
	return false
}

// P4ChangelistWarning is one thing the daemon noticed about a changelist's
// files that the person submitting it has to deal with — a +l file someone else
// holds, a file that is not at head.
type P4ChangelistWarning struct {
	Kind  string   `json:"kind"`
	Files []string `json:"files"`
}

// P4ChangelistResponse is one changelist on the wire. Field-for-field the
// `P4Changelist` in packages/core/types/p4.ts.
type P4ChangelistResponse struct {
	ID                string                `json:"id"`
	WorkspaceID       string                `json:"workspace_id"`
	Port              string                `json:"port"`
	Number            int32                 `json:"number"`
	OriginalNumber    int32                 `json:"original_number"`
	Status            string                `json:"status"`
	Client            string                `json:"client"`
	P4User            string                `json:"p4_user"`
	Description       string                `json:"description"`
	Ticket            string                `json:"ticket"`
	FilesCount        int32                 `json:"files_count"`
	ExclusiveFiles    []string              `json:"exclusive_files"`
	Warnings          []P4ChangelistWarning `json:"warnings"`
	SwarmReviewID     *string               `json:"swarm_review_id"`
	SwarmReviewNumber *int32                `json:"swarm_review_number"`
	SwarmReviewURL    *string               `json:"swarm_review_url"`
	SwarmReviewState  *string               `json:"swarm_review_state"`
	TaskID            *string               `json:"task_id"`
	LinkSource        string                `json:"link_source"`
	SubmittedAt       *string               `json:"submitted_at"`
	CreatedAt         string                `json:"created_at"`
	UpdatedAt         string                `json:"updated_at"`
}

func p4ChangelistRowToResponse(row db.ListP4ChangelistsForIssueRow) P4ChangelistResponse {
	return P4ChangelistResponse{
		ID:                uuidToString(row.ID),
		WorkspaceID:       uuidToString(row.WorkspaceID),
		Port:              row.Port,
		Number:            row.Number,
		OriginalNumber:    row.OriginalNumber,
		Status:            row.Status,
		Client:            row.Client,
		P4User:            row.P4User,
		Description:       row.Description,
		Ticket:            row.Ticket,
		FilesCount:        row.FilesCount,
		ExclusiveFiles:    decodeP4StringList(row.ExclusiveFiles),
		Warnings:          decodeP4Warnings(row.Warnings),
		SwarmReviewID:     uuidPtr(row.SwarmReviewID),
		SwarmReviewNumber: int4Ptr(row.SwarmReviewNumber),
		SwarmReviewURL:    textPtr(row.SwarmReviewUrl),
		SwarmReviewState:  textPtr(row.SwarmReviewState),
		TaskID:            uuidPtr(row.TaskID),
		LinkSource:        row.LinkSource,
		SubmittedAt:       timestampToPtr(row.SubmittedAt),
		CreatedAt:         timestampToString(row.CreatedAt),
		UpdatedAt:         timestampToString(row.UpdatedAt),
	}
}

// decodeP4StringList and decodeP4Warnings fail open to an empty list: a
// changelist card with no warnings is right far more often than a 500 on a
// column the daemon wrote.
func decodeP4StringList(raw []byte) []string {
	out := []string{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func decodeP4Warnings(raw []byte) []P4ChangelistWarning {
	out := []P4ChangelistWarning{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []P4ChangelistWarning{}
	}
	for i := range out {
		if out[i].Files == nil {
			out[i].Files = []string{}
		}
	}
	return out
}

func uuidPtr(id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	s := uuidToString(id)
	return &s
}

func int4Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}

func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// p4ServerKey identifies the Helix server a changelist number belongs to. The
// daemon sends the serverCluster when the server reports one; an edge server
// answers on its own port while numbering the commit server's changelists, so
// the port is only a fallback.
func p4ServerKey(serverKey, port string) string {
	if key := strings.TrimSpace(serverKey); key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(port))
}

// ── Daemon routes ───────────────────────────────────────────────────────────

type p4SwarmReviewReport struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	Author      string `json:"author"`
	Description string `json:"description"`
}

type reportTaskP4ChangelistRequest struct {
	Port           string                `json:"port"`
	ServerKey      string                `json:"server_key"`
	Charset        string                `json:"charset"`
	Number         int32                 `json:"number"`
	OriginalNumber int32                 `json:"original_number"`
	Status         string                `json:"status"`
	Client         string                `json:"client"`
	P4User         string                `json:"p4_user"`
	Description    string                `json:"description"`
	Ticket         string                `json:"ticket"`
	FilesCount     int32                 `json:"files_count"`
	ExclusiveFiles []string              `json:"exclusive_files"`
	Warnings       []P4ChangelistWarning `json:"warnings"`
	SwarmReview    *p4SwarmReviewReport  `json:"swarm_review"`
}

// ReportTaskP4Changelist (POST /api/daemon/tasks/{taskId}/p4/changelists)
// records the changelist a task produced and links it to the task's issue.
// Idempotent on (workspace_id, server_key, original_number), so the daemon can
// report the same changelist after every write without creating a second row.
func (h *Handler) ReportTaskP4Changelist(w http.ResponseWriter, r *http.Request) {
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if !task.IssueID.Valid {
		// Workspace mode binds through an issue's project resources, so a task
		// without an issue has no changelist to report. Saying so beats storing
		// a row nothing can ever show.
		writeError(w, http.StatusBadRequest, "task is not bound to an issue")
		return
	}
	var req reportTaskP4ChangelistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	port := strings.TrimSpace(req.Port)
	if port == "" {
		writeError(w, http.StatusBadRequest, "port is required")
		return
	}
	if req.Number <= 0 {
		writeError(w, http.StatusBadRequest, "number must be a positive changelist number")
		return
	}
	original := req.OriginalNumber
	if original <= 0 {
		// A changelist that has never been submitted is its own original.
		original = req.Number
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = p4ChangelistStatusPending
	}
	if !validP4ChangelistStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be pending, shelved, submitted or deleted")
		return
	}
	// Resolved from the task row, not from the request.
	wsUUID := parseUUID(workspaceID)
	tx, qtx, err := h.beginP4ChangelistWrite(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save changelist")
		return
	}
	defer tx.Rollback(r.Context())
	serverKey := p4ServerKey(req.ServerKey, port)
	if _, _, err := reconcileP4ChangelistIdentity(r.Context(), qtx, wsUUID, serverKey, port, req.Number, original, pgtype.UUID{}); err != nil {
		slog.Error("resolve p4 changelist identity", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save changelist")
		return
	}

	var reviewID pgtype.UUID
	if req.SwarmReview != nil && req.SwarmReview.Number > 0 {
		reviewID, ok = h.upsertP4SwarmReview(r.Context(), w, qtx, wsUUID, req.Number, *req.SwarmReview)
		if !ok {
			return
		}
	}

	exclusive, warnings, ok := encodeP4Lists(w, req.ExclusiveFiles, req.Warnings)
	if !ok {
		return
	}
	var submittedAt pgtype.Timestamptz
	if status == p4ChangelistStatusSubmitted {
		submittedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	row, err := qtx.UpsertP4Changelist(r.Context(), db.UpsertP4ChangelistParams{
		WorkspaceID:    wsUUID,
		DaemonID:       middleware.DaemonIDFromContext(r.Context()),
		ServerKey:      serverKey,
		Port:           port,
		Charset:        strings.TrimSpace(req.Charset),
		Number:         req.Number,
		OriginalNumber: original,
		Client:         strings.TrimSpace(req.Client),
		P4User:         strings.TrimSpace(req.P4User),
		Status:         status,
		Description:    req.Description,
		Ticket:         strings.TrimSpace(req.Ticket),
		FilesCount:     req.FilesCount,
		ExclusiveFiles: exclusive,
		Warnings:       warnings,
		SwarmReviewID:  reviewID,
		TaskID:         task.ID,
		SubmittedAt:    submittedAt,
	})
	if err != nil {
		slog.Error("upsert p4 changelist", "error", err, "task_id", uuidToString(task.ID))
		writeError(w, http.StatusInternalServerError, "failed to save changelist")
		return
	}
	if err := qtx.LinkIssueP4Changelist(r.Context(), db.LinkIssueP4ChangelistParams{
		IssueID:      task.IssueID,
		ChangelistID: row.ID,
		WorkspaceID:  wsUUID,
		LinkSource:   p4ChangelistLinkTask,
	}); err != nil {
		slog.Error("link p4 changelist", "error", err, "task_id", uuidToString(task.ID))
		writeError(w, http.StatusInternalServerError, "failed to link changelist")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save changelist")
		return
	}
	h.afterP4ChangelistStatusChange(r.Context(), wsUUID, row)

	resp, ok := h.p4ChangelistForIssue(r.Context(), task.IssueID, wsUUID, row.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load changelist")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// upsertP4SwarmReview records the review the daemon created for this changelist
// and returns its id so the changelist can carry it. The review is deliberately
// NOT linked to the issue through issue_swarm_review: it belongs to a
// changelist card, and a second card in the pull-request list would show the
// same review twice.
func (h *Handler) upsertP4SwarmReview(
	ctx context.Context,
	w http.ResponseWriter,
	q *db.Queries,
	workspaceID pgtype.UUID,
	changelistNumber int32,
	review p4SwarmReviewReport,
) (pgtype.UUID, bool) {
	htmlURL := strings.TrimSpace(review.URL)
	if htmlURL == "" {
		writeError(w, http.StatusBadRequest, "swarm_review.url is required")
		return pgtype.UUID{}, false
	}
	base := swarmInstanceURL(htmlURL)
	state := strings.TrimSpace(review.State)
	if state == "" {
		state = "needsReview"
	}
	desc := strings.TrimSpace(review.Description)
	saved, err := q.UpsertSwarmReview(ctx, db.UpsertSwarmReviewParams{
		WorkspaceID:  workspaceID,
		SwarmUrl:     base,
		ReviewNumber: int32(review.Number),
		Changelist:   strconv.Itoa(int(changelistNumber)),
		Title:        swarmReviewTitle(desc, review.Number),
		Description:  desc,
		Author:       strings.TrimSpace(review.Author),
		State:        state,
		HtmlUrl:      htmlURL,
	})
	if err != nil {
		slog.Error("upsert swarm review for p4 changelist", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save swarm review")
		return pgtype.UUID{}, false
	}
	return saved.ID, true
}

// swarmInstanceURL recovers the Swarm instance URL that swarm_review keys on
// from a review's own URL (".../reviews/123" → the instance).
func swarmInstanceURL(reviewURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(reviewURL), "/")
	if i := strings.Index(trimmed, "/reviews/"); i > 0 {
		return trimmed[:i]
	}
	return trimmed
}

func encodeP4Lists(w http.ResponseWriter, files []string, warnings []P4ChangelistWarning) ([]byte, []byte, bool) {
	if files == nil {
		files = []string{}
	}
	if warnings == nil {
		warnings = []P4ChangelistWarning{}
	}
	encodedFiles, err := json.Marshal(files)
	if err == nil {
		var encodedWarnings []byte
		encodedWarnings, err = json.Marshal(warnings)
		if err == nil {
			return encodedFiles, encodedWarnings, true
		}
	}
	writeError(w, http.StatusBadRequest, "exclusive_files and warnings must be JSON-encodable")
	return nil, nil, false
}

// ListOpenP4ChangelistsForDaemon (GET /api/daemon/p4/changelists/open) is the
// poll list: unfinished changelists this daemon reported, plus rows no daemon
// owns yet. A manually linked changelist is one of the unowned rows — the
// daemon recognizes the port it serves and claims the row with its first status
// report.
func (h *Handler) ListOpenP4ChangelistsForDaemon(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	rows, err := h.Queries.ListOpenP4ChangelistsForDaemon(r.Context(), db.ListOpenP4ChangelistsForDaemonParams{
		WorkspaceID: wsUUID,
		DaemonID:    middleware.DaemonIDFromContext(r.Context()),
	})
	if err != nil {
		slog.Error("list open p4 changelists", "error", err, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "failed to list changelists")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	depots, err := h.p4PollingDepots(r.Context(), wsUUID, rows)
	if err != nil {
		slog.Error("resolve p4 polling resources", "error", err, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "failed to load Perforce resources")
		return
	}
	for _, row := range rows {
		allowed := false
		for _, depot := range depots {
			if strings.EqualFold(depot.Port, row.Port) {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		out = append(out, map[string]any{
			"id":                  uuidToString(row.ID),
			"port":                row.Port,
			"number":              row.Number,
			"original_number":     row.OriginalNumber,
			"status":              row.Status,
			"swarm_review_number": int4Ptr(row.SwarmReviewNumber),
			"swarm_url":           textPtr(row.SwarmUrl),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changelists": out, "p4_depots": depots})
}

// Project depots do not belong in the daemon's workspace-wide task allowlist.
// Resolve them specifically for polling, including after a task has ended or
// the daemon has restarted and no claim-time resources remain in memory.
func (h *Handler) p4PollingDepots(ctx context.Context, workspaceID pgtype.UUID, rows []db.ListOpenP4ChangelistsForDaemonRow) ([]P4DepotData, error) {
	resources := map[string]P4DepotData{}
	projects := map[string]bool{}
	for _, row := range rows {
		issues, err := h.Queries.ListIssueIDsForP4Changelist(ctx, db.ListIssueIDsForP4ChangelistParams{WorkspaceID: workspaceID, ChangelistID: row.ID})
		if err != nil {
			return nil, err
		}
		for _, issueID := range issues {
			issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			projectKey := uuidToString(issue.ProjectID)
			if projects[projectKey] {
				continue
			}
			project, err := h.resolveClaimProjectContext(ctx, issue.ProjectID, workspaceID, nil)
			if err != nil {
				return nil, err
			}
			projects[projectKey] = true
			for _, depot := range project.P4Depots {
				ref, err := p4depot.Normalize(p4depot.Ref(depot))
				if err != nil || strings.HasPrefix(strings.ToLower(ref.Port), "rsh:") {
					continue
				}
				resources[p4depot.Identity(ref)] = P4DepotData(ref)
			}
		}
	}
	keys := make([]string, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]P4DepotData, 0, len(keys))
	for _, key := range keys {
		out = append(out, resources[key])
	}
	return out, nil
}

type p4ChangelistStatusUpdate struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Number           int32  `json:"number"`
	OriginalNumber   int32  `json:"original_number"`
	ServerKey        string `json:"server_key"`
	SubmittedAt      string `json:"submitted_at"`
	SwarmReviewState string `json:"swarm_review_state"`
}

type updateP4ChangelistStatusesRequest struct {
	WorkspaceID string                     `json:"workspace_id"`
	Updates     []p4ChangelistStatusUpdate `json:"updates"`
}

// UpdateP4ChangelistStatuses (POST /api/daemon/p4/changelists/status) applies
// one poll round. Rows the daemon may not touch are skipped rather than
// failing the batch, so one foreign id cannot stall the whole poll.
func (h *Handler) UpdateP4ChangelistStatuses(w http.ResponseWriter, r *http.Request) {
	var req updateP4ChangelistStatusesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	daemonID := middleware.DaemonIDFromContext(r.Context())

	updated := 0
	for _, update := range req.Updates {
		id, ok := parseUUIDOrBadRequest(w, update.ID, "changelist id")
		if !ok {
			return
		}
		status := strings.TrimSpace(update.Status)
		if !validP4ChangelistStatus(status) {
			writeError(w, http.StatusBadRequest, "status must be pending, shelved, submitted or deleted")
			return
		}
		var submittedAt pgtype.Timestamptz
		if raw := strings.TrimSpace(update.SubmittedAt); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "submitted_at must be an RFC3339 timestamp")
				return
			}
			submittedAt = pgtype.Timestamptz{Time: parsed, Valid: true}
		} else if status == p4ChangelistStatusSubmitted {
			submittedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		}

		update.Status = status
		row, err := h.applyP4ChangelistStatus(r.Context(), wsUUID, id, daemonID, update, submittedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			// Gone, in another workspace, or owned by a different daemon.
			continue
		}
		if err != nil {
			slog.Error("update p4 changelist status", "error", err, "changelist_id", update.ID)
			writeError(w, http.StatusInternalServerError, "failed to update changelists")
			return
		}
		updated++
		h.afterP4ChangelistStatusChange(r.Context(), wsUUID, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

// afterP4ChangelistStatusChange tells every issue holding this changelist, and
// runs the auto-advance for them once the changelist is submitted.
func (h *Handler) afterP4ChangelistStatusChange(ctx context.Context, workspaceID pgtype.UUID, row db.P4Changelist) {
	issueIDs, err := h.Queries.ListIssueIDsForP4Changelist(ctx, db.ListIssueIDsForP4ChangelistParams{
		ChangelistID: row.ID,
		WorkspaceID:  workspaceID,
	})
	if err != nil {
		slog.Warn("list issues for p4 changelist", "error", err, "changelist_id", uuidToString(row.ID))
		return
	}
	for _, issueID := range issueIDs {
		h.publishP4ChangelistUpdated(uuidToString(workspaceID), issueID, row.ID)
		if row.Status == p4ChangelistStatusSubmitted {
			h.maybeAutoCompleteIssueFromP4(ctx, workspaceID, issueID)
		}
	}
}

// ── User routes ─────────────────────────────────────────────────────────────

// ListP4ChangelistsForIssue (GET /api/issues/{id}/p4-changelists).
func (h *Handler) ListP4ChangelistsForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out, err := h.p4ChangelistsForIssue(r.Context(), issue.ID, issue.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list changelists")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changelists": out})
}

type linkIssueP4ChangelistRequest struct {
	Number int32  `json:"number"`
	Port   string `json:"port"`
}

// LinkIssueP4Changelist (POST /api/issues/{id}/p4-changelists) records a
// changelist number a person typed. Everything else about it — status, files,
// review — stays unknown until a daemon that serves the port picks the row up
// on its next poll, so the row starts out pending.
func (h *Handler) LinkIssueP4Changelist(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req linkIssueP4ChangelistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Number <= 0 {
		writeError(w, http.StatusBadRequest, "number must be a positive changelist number")
		return
	}
	port, ok := h.resolveIssueP4Port(r.Context(), issue, req.Port)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity,
			"port is required: this issue's project does not have exactly one Perforce depot to take it from")
		return
	}
	tx, qtx, err := h.beginP4ChangelistWrite(r.Context(), issue.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link changelist")
		return
	}
	defer tx.Rollback(r.Context())
	existing, err := qtx.FindP4ChangelistsByPortNumber(r.Context(), db.FindP4ChangelistsByPortNumberParams{
		WorkspaceID: issue.WorkspaceID, Port: port, Number: req.Number,
	})
	var row db.P4Changelist
	if err == nil && len(existing) > 0 {
		row = existing[0]
		for _, duplicate := range existing[1:] {
			if err = qtx.MergeP4Changelist(r.Context(), db.MergeP4ChangelistParams{WorkspaceID: issue.WorkspaceID, TargetID: row.ID, SourceID: duplicate.ID}); err != nil {
				break
			}
		}
	} else if err == nil {
		row, err = qtx.InsertManualP4Changelist(r.Context(), db.InsertManualP4ChangelistParams{
			WorkspaceID: issue.WorkspaceID, ServerKey: p4ServerKey("", port), Port: port, Number: req.Number,
		})
	}
	if err != nil {
		slog.Error("insert manual p4 changelist", "error", err, "issue_id", uuidToString(issue.ID))
		writeError(w, http.StatusInternalServerError, "failed to link changelist")
		return
	}
	if err := qtx.LinkIssueP4Changelist(r.Context(), db.LinkIssueP4ChangelistParams{
		IssueID:      issue.ID,
		ChangelistID: row.ID,
		WorkspaceID:  issue.WorkspaceID,
		LinkSource:   p4ChangelistLinkManual,
	}); err != nil {
		slog.Error("link manual p4 changelist", "error", err, "issue_id", uuidToString(issue.ID))
		writeError(w, http.StatusInternalServerError, "failed to link changelist")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link changelist")
		return
	}
	h.afterP4ChangelistStatusChange(r.Context(), issue.WorkspaceID, row)

	resp, ok := h.p4ChangelistForIssue(r.Context(), issue.ID, issue.WorkspaceID, row.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load changelist")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// UnlinkIssueP4Changelist (DELETE /api/issues/{id}/p4-changelists/{changelistId})
// drops the link. A changelist nobody holds any more is deleted with it, so it
// stops appearing in daemon polls.
func (h *Handler) UnlinkIssueP4Changelist(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	changelistID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "changelistId"), "changelist id")
	if !ok {
		return
	}
	tx, qtx, err := h.beginP4ChangelistWrite(r.Context(), issue.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unlink changelist")
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := qtx.UnlinkIssueP4Changelist(r.Context(), db.UnlinkIssueP4ChangelistParams{
		IssueID:      issue.ID,
		ChangelistID: changelistID,
		WorkspaceID:  issue.WorkspaceID,
	})
	if err != nil {
		slog.Error("unlink p4 changelist", "error", err, "issue_id", uuidToString(issue.ID))
		writeError(w, http.StatusInternalServerError, "failed to unlink changelist")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "changelist not linked to this issue")
		return
	}
	if err := qtx.DeleteOrphanP4Changelist(r.Context(), db.DeleteOrphanP4ChangelistParams{
		ID:          changelistID,
		WorkspaceID: issue.WorkspaceID,
	}); err != nil {
		slog.Warn("delete orphan p4 changelist", "error", err, "changelist_id", uuidToString(changelistID))
		writeError(w, http.StatusInternalServerError, "failed to unlink changelist")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unlink changelist")
		return
	}
	h.publishP4ChangelistUpdated(uuidToString(issue.WorkspaceID), issue.ID, changelistID)
	// Removing the last unsubmitted changelist can leave only submitted ones.
	h.maybeAutoCompleteIssueFromP4(r.Context(), issue.WorkspaceID, issue.ID)
	w.WriteHeader(http.StatusNoContent)
}

// resolveIssueP4Port takes the port the request gave, or the one depot the
// issue's project resolves to. Zero or several depots means the issue cannot
// name a server on its own and the request has to.
func (h *Handler) resolveIssueP4Port(ctx context.Context, issue db.Issue, requested string) (string, bool) {
	if port := strings.TrimSpace(requested); port != "" {
		return port, true
	}
	projectCtx, err := h.resolveClaimProjectContext(ctx, issue.ProjectID, issue.WorkspaceID, nil)
	if err != nil {
		slog.Warn("resolve p4 port: load project context failed", "error", err, "issue_id", uuidToString(issue.ID))
		return "", false
	}
	if len(projectCtx.P4Depots) != 1 {
		return "", false
	}
	port := strings.TrimSpace(projectCtx.P4Depots[0].Port)
	return port, port != ""
}

// ── Shared reads ────────────────────────────────────────────────────────────

func (h *Handler) p4ChangelistsForIssue(ctx context.Context, issueID, workspaceID pgtype.UUID) ([]P4ChangelistResponse, error) {
	rows, err := h.Queries.ListP4ChangelistsForIssue(ctx, db.ListP4ChangelistsForIssueParams{
		IssueID:     issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Error("list p4 changelists for issue", "error", err, "issue_id", uuidToString(issueID))
		return nil, err
	}
	out := make([]P4ChangelistResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, p4ChangelistRowToResponse(row))
	}
	return out, nil
}

// p4ChangelistForIssue re-reads one changelist through the list query so a
// create response and a later list render byte-identical rows.
func (h *Handler) p4ChangelistForIssue(ctx context.Context, issueID, workspaceID, changelistID pgtype.UUID) (P4ChangelistResponse, bool) {
	all, err := h.p4ChangelistsForIssue(ctx, issueID, workspaceID)
	if err != nil {
		return P4ChangelistResponse{}, false
	}
	id := uuidToString(changelistID)
	for _, cl := range all {
		if cl.ID == id {
			return cl, true
		}
	}
	return P4ChangelistResponse{}, false
}

// p4ChangelistSwarmReviewIDs lists the reviews an issue already shows as a
// changelist card.
func (h *Handler) p4ChangelistSwarmReviewIDs(ctx context.Context, issue db.Issue) map[string]struct{} {
	ids, err := h.Queries.ListIssueP4ChangelistSwarmReviewIDs(ctx, db.ListIssueP4ChangelistSwarmReviewIDsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("list p4 changelist swarm reviews", "error", err, "issue_id", uuidToString(issue.ID))
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[uuidToString(id)] = struct{}{}
	}
	return out
}

func (h *Handler) publishP4ChangelistUpdated(workspaceID string, issueID, changelistID pgtype.UUID) {
	h.publish(protocol.EventP4ChangelistUpdated, workspaceID, "system", "", map[string]any{
		"issue_id":      uuidToString(issueID),
		"changelist_id": uuidToString(changelistID),
	})
}

// ── Auto-advance ────────────────────────────────────────────────────────────

// maybeAutoCompleteIssueFromP4 moves an issue to the workspace's merge target
// once every changelist linked to it is submitted.
//
// It is the Perforce-only half of the PR merge automation and shares its
// settings: the workspace's merge target and the per-issue opt-out both apply.
// An issue that also has a GitHub or self-hosted PR linked is left to
// maybeAutoCompleteIssue — a reviewed PR owns that issue's completion, and two
// rules writing the same status would race. The upstream decision's PR union is
// read, never extended.
func (h *Handler) maybeAutoCompleteIssueFromP4(ctx context.Context, workspaceID, issueID pgtype.UUID) {
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("p4 auto-complete: load issue failed", "err", err, "issue_id", uuidToString(issueID))
		}
		return
	}
	prs, err := h.Queries.ListIssueLinkedPullRequestStates(ctx, issue.ID)
	if err != nil {
		slog.Warn("p4 auto-complete: load linked pull requests failed", "err", err, "issue_id", uuidToString(issueID))
		return
	}
	if len(prs) > 0 {
		return
	}
	changelists, err := h.Queries.ListP4ChangelistStatesForIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("p4 auto-complete: load changelists failed", "err", err, "issue_id", uuidToString(issueID))
		return
	}
	if len(changelists) == 0 {
		return
	}
	for _, cl := range changelists {
		if cl.Status != p4ChangelistStatusSubmitted {
			return
		}
	}
	disabled, err := h.Queries.GetIssuePRAutoCompleteDisabled(ctx, issue.ID)
	if err != nil {
		slog.Warn("p4 auto-complete: load issue opt-out failed", "err", err, "issue_id", uuidToString(issueID))
		return
	}
	if disabled {
		return
	}
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("p4 auto-complete: load workspace failed", "err", err, "workspace_id", uuidToString(workspaceID))
		return
	}
	resolver := issuestatus.NewResolver(issue.WorkspaceID)
	switch resolver.Effective(ctx, h.issueStatusCatalog(), issue.Status) {
	case issuestatus.Done, issuestatus.Cancelled:
		return
	}
	if issue.TriageState.Valid {
		return
	}
	target := h.resolvePRMergeTarget(ctx, ws, resolver)
	if target == "" || issue.Status == target {
		return
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("p4 auto-complete: begin failed", "err", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.MoveIssueFromP4Changelists(ctx, db.MoveIssueFromP4ChangelistsParams{
		ID:             issue.ID,
		WorkspaceID:    issue.WorkspaceID,
		TargetStatus:   target,
		ExpectedStatus: issue.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Someone changed the status, or linked work, since the decision.
		return
	}
	var cancelledWakeups []db.AgentTaskQueue
	if err == nil {
		cancelledWakeups, err = service.StopClosedIssueWakeups(ctx, qtx, updated)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.Warn("p4 auto-complete: status write failed", "err", err, "issue_id", uuidToString(issueID))
		return
	}
	h.broadcastCancelledWakeups(ctx, updated.WorkspaceID, cancelledWakeups)
	if updated.ParentIssueID.Valid {
		h.processChildEvents(ctx, updated.ParentIssueID)
	}

	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, updated.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, uuidToString(workspaceID), "system", "", map[string]any{
		"issue":                      resp,
		"status_changed":             true,
		"prev_status":                issue.Status,
		"creator_type":               issue.CreatorType,
		"creator_id":                 uuidToString(issue.CreatorID),
		"source":                     "p4_automation",
		"changelists":                p4ChangelistNumberList(changelists),
		"duplicate_of_issue_id":      liveDuplicateMark(updated.Status, updated.DuplicateOfIssueID),
		"prev_duplicate_of_issue_id": liveDuplicateMark(issue.Status, issue.DuplicateOfIssueID),
	})
}

// p4ChangelistNumberList renders "CL 795095, CL 795102" for the activity entry.
func p4ChangelistNumberList(changelists []db.ListP4ChangelistStatesForIssueRow) string {
	parts := make([]string, 0, len(changelists))
	for _, cl := range changelists {
		parts = append(parts, "CL "+strconv.Itoa(int(cl.OriginalNumber)))
	}
	return strings.Join(parts, ", ")
}
