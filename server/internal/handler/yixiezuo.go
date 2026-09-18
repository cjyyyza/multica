package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type yixiezuoConnectionResponse struct {
	ID                string            `json:"id"`
	WorkspaceID       string            `json:"workspace_id"`
	ProjectID         *string           `json:"project_id"`
	CLIBin            string            `json:"cli_bin"`
	GCPHost           string            `json:"gcp_host"`
	ListQueryID       string            `json:"list_query_id"`
	ExternalProjectID string            `json:"external_project_id"`
	TrackerID         string            `json:"tracker_id"`
	StatusMap         map[string]string `json:"status_map"`
	LastPulledAt      *string           `json:"last_pulled_at"`
	LastPushedAt      *string           `json:"last_pushed_at"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
}

type yixiezuoConnectionEnvelope struct {
	Connection *yixiezuoConnectionResponse `json:"connection"`
	CanManage  bool                        `json:"can_manage"`
}

type upsertYixiezuoConnectionRequest struct {
	ProjectID         *string           `json:"project_id"`
	CLIBin            string            `json:"cli_bin"`
	GCPHost           string            `json:"gcp_host"`
	ListQueryID       string            `json:"list_query_id"`
	ExternalProjectID string            `json:"external_project_id"`
	TrackerID         string            `json:"tracker_id"`
	StatusMap         map[string]string `json:"status_map"`
}

type yixiezuoCardPayload struct {
	ExternalID  string `json:"external_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	StatusName  string `json:"status_name"`
	Priority    string `json:"priority"`
	StartDate   string `json:"start_date"`
	DueDate     string `json:"due_date"`
	UpdatedAt   string `json:"updated_at"`
}

type yixiezuoPullRequest struct {
	Cards []yixiezuoCardPayload `json:"cards"`
}

type yixiezuoPullResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

type yixiezuoExportIssue struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	StartDate   *string `json:"start_date"`
	DueDate     *string `json:"due_date"`
	UpdatedAt   string  `json:"updated_at"`
	Revision    int64   `json:"revision"`
}

type yixiezuoExportItem struct {
	Issue           yixiezuoExportIssue `json:"issue"`
	ExternalIssueID string              `json:"external_issue_id,omitempty"`
	StatusName      string              `json:"status_name"`
}

type yixiezuoExportResponse struct {
	Connection yixiezuoConnectionResponse `json:"connection"`
	Updates    []yixiezuoExportItem       `json:"updates"`
	Creates    []yixiezuoExportItem       `json:"creates"`
}

type yixiezuoPushAckItem struct {
	IssueID           string `json:"issue_id"`
	ExternalIssueID   string `json:"external_issue_id"`
	ExternalUpdatedAt string `json:"external_updated_at"`
	Error             string `json:"error"`
}

type yixiezuoPushAckRequest struct {
	Results []yixiezuoPushAckItem `json:"results"`
}

func (h *Handler) GetYixiezuoConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	row, err := h.Queries.GetYixiezuoConnectionByWorkspace(r.Context(), wsUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, yixiezuoConnectionEnvelope{
				CanManage: roleAllowed(member.Role, "owner", "admin"),
			})
			return
		}
		slog.Warn("get yixiezuo connection failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load 易协作 connection")
		return
	}
	resp := yixiezuoConnectionToResponse(row)
	writeJSON(w, http.StatusOK, yixiezuoConnectionEnvelope{
		Connection: &resp,
		CanManage:  roleAllowed(member.Role, "owner", "admin"),
	})
}

func (h *Handler) UpsertYixiezuoConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	var req upsertYixiezuoConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cliBin := strings.TrimSpace(req.CLIBin)
	if cliBin == "" {
		cliBin = "popo-cli"
	}
	listQueryID := strings.TrimSpace(req.ListQueryID)
	if listQueryID == "" {
		writeError(w, http.StatusBadRequest, "list_query_id is required; unscoped 易协作 kanban pulls are refused")
		return
	}
	projectID := pgtype.UUID{}
	if req.ProjectID != nil && strings.TrimSpace(*req.ProjectID) != "" {
		parsed, parsedOK := parseUUIDOrBadRequest(w, strings.TrimSpace(*req.ProjectID), "project_id")
		if !parsedOK {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: parsed, WorkspaceID: wsUUID,
		}); err != nil {
			if isNotFound(err) {
				writeError(w, http.StatusBadRequest, "project not found in this workspace")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to validate project")
			return
		}
		projectID = parsed
	}
	statusMap := req.StatusMap
	if statusMap == nil {
		statusMap = map[string]string{}
	}
	rawMap, err := json.Marshal(statusMap)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid status_map")
		return
	}
	row, err := h.Queries.UpsertYixiezuoConnection(r.Context(), db.UpsertYixiezuoConnectionParams{
		WorkspaceID:       wsUUID,
		CliBin:            cliBin,
		GcpHost:           strings.TrimSpace(req.GCPHost),
		ListQueryID:       listQueryID,
		ExternalProjectID: strings.TrimSpace(req.ExternalProjectID),
		TrackerID:         strings.TrimSpace(req.TrackerID),
		StatusMap:         rawMap,
		ProjectID:         projectID,
		CreatedByID:       member.UserID,
	})
	if err != nil {
		slog.Warn("upsert yixiezuo connection failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save 易协作 connection")
		return
	}
	resp := yixiezuoConnectionToResponse(row)
	writeJSON(w, http.StatusOK, yixiezuoConnectionEnvelope{
		Connection: &resp,
		CanManage:  true,
	})
}

func (h *Handler) DeleteYixiezuoConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	row, err := h.Queries.GetYixiezuoConnectionByWorkspace(r.Context(), wsUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load 易协作 connection")
		return
	}
	if err := h.Queries.DeleteYixiezuoConnection(r.Context(), db.DeleteYixiezuoConnectionParams{
		ID: row.ID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete 易协作 connection")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) PullYixiezuoCards(w http.ResponseWriter, r *http.Request) {
	wsUUID, member, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	conn, ok := h.requireYixiezuoConnection(w, r, wsUUID)
	if !ok {
		return
	}
	var req yixiezuoPullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	statusMap := decodeYixiezuoStatusMap(conn.StatusMap)
	catalogKeys, catalogNames := h.yixiezuoCatalog(r.Context(), wsUUID)
	userID := uuidToString(member.UserID)
	result := yixiezuoPullResult{}
	for _, raw := range req.Cards {
		card := yixiezuoCardFromPayload(raw)
		if card.ExternalID == "" || strings.TrimSpace(card.Title) == "" {
			result.Skipped++
			continue
		}
		_, linkErr := h.Queries.GetYixiezuoCardLinkByExternal(r.Context(), db.GetYixiezuoCardLinkByExternalParams{
			ConnectionID: conn.ID, ExternalIssueID: card.ExternalID,
		})
		existed := linkErr == nil
		if err := h.applyYixiezuoCard(r.Context(), conn, card, statusMap, catalogKeys, catalogNames, userID); err != nil {
			if errors.Is(err, errYixiezuoSkip) {
				result.Skipped++
				continue
			}
			slog.Warn("apply yixiezuo card failed", append(logger.RequestAttrs(r), "error", err, "external_id", card.ExternalID)...)
			writeError(w, http.StatusInternalServerError, "failed to apply 易协作 card")
			return
		}
		if existed {
			result.Updated++
		} else {
			result.Created++
		}
	}
	if _, err := h.Queries.TouchYixiezuoConnectionPull(r.Context(), db.TouchYixiezuoConnectionPullParams{
		ID: conn.ID, WorkspaceID: wsUUID,
	}); err != nil {
		slog.Warn("touch yixiezuo pull failed", append(logger.RequestAttrs(r), "error", err)...)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ExportYixiezuoChanges(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	conn, ok := h.requireYixiezuoConnection(w, r, wsUUID)
	if !ok {
		return
	}
	statusMap := decodeYixiezuoStatusMap(conn.StatusMap)
	_, catalogNames := h.yixiezuoCatalog(r.Context(), wsUUID)
	dirty, err := h.Queries.ListDirtyYixiezuoCardLinks(r.Context(), conn.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dirty 易协作 links")
		return
	}
	updates := make([]yixiezuoExportItem, 0, len(dirty))
	for _, link := range dirty {
		issue, issueErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID: link.IssueID, WorkspaceID: wsUUID,
		})
		if issueErr != nil {
			continue
		}
		updates = append(updates, yixiezuoExportItem{
			Issue:           yixiezuoExportFromIssue(issue),
			ExternalIssueID: link.ExternalIssueID,
			StatusName:      yixiezuo.ResolveOutgoingStatusName(issue.Status, statusMap, catalogNames),
		})
	}
	creates := []yixiezuoExportItem{}
	if conn.ProjectID.Valid {
		rows, listErr := h.Queries.ListUnlinkedIssuesForYixiezuoExport(r.Context(), db.ListUnlinkedIssuesForYixiezuoExportParams{
			WorkspaceID: wsUUID, ConnectionID: conn.ID, ProjectID: conn.ProjectID,
		})
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to list unlinked issues")
			return
		}
		for _, row := range rows {
			creates = append(creates, yixiezuoExportItem{
				Issue: yixiezuoExportIssue{
					ID:          uuidToString(row.ID),
					Title:       row.Title,
					Description: stringOrEmpty(row.Description),
					Status:      row.Status,
					Priority:    row.Priority,
					StartDate:   dateToPtr(row.StartDate),
					DueDate:     dateToPtr(row.DueDate),
					UpdatedAt:   timestampToString(row.UpdatedAt),
					Revision:    row.Revision,
				},
				StatusName: yixiezuo.ResolveOutgoingStatusName(row.Status, statusMap, catalogNames),
			})
		}
	}
	writeJSON(w, http.StatusOK, yixiezuoExportResponse{
		Connection: yixiezuoConnectionToResponse(conn),
		Updates:    updates,
		Creates:    creates,
	})
}

func (h *Handler) AckYixiezuoPush(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := h.yixiezuoWorkspace(w, r)
	if !ok {
		return
	}
	conn, ok := h.requireYixiezuoConnection(w, r, wsUUID)
	if !ok {
		return
	}
	var req yixiezuoPushAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, item := range req.Results {
		issueUUID, parsedOK := parseUUIDOrBadRequest(w, item.IssueID, "issue_id")
		if !parsedOK {
			return
		}
		if strings.TrimSpace(item.ExternalIssueID) == "" {
			writeError(w, http.StatusBadRequest, "external_issue_id is required")
			return
		}
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID: issueUUID, WorkspaceID: wsUUID,
		})
		if err != nil {
			if isNotFound(err) {
				continue
			}
			writeError(w, http.StatusInternalServerError, "failed to load issue")
			return
		}
		extUpdated := pgtype.Timestamptz{}
		if ts, tsErr := time.Parse(time.RFC3339, strings.TrimSpace(item.ExternalUpdatedAt)); tsErr == nil {
			extUpdated = pgtype.Timestamptz{Time: ts, Valid: true}
		}
		if _, err := h.Queries.UpsertYixiezuoCardLink(r.Context(), db.UpsertYixiezuoCardLinkParams{
			WorkspaceID:       wsUUID,
			ConnectionID:      conn.ID,
			IssueID:           issue.ID,
			ExternalIssueID:   strings.TrimSpace(item.ExternalIssueID),
			IssueRevision:     issue.Revision,
			Dirty:             item.Error != "",
			LastDirection:     "push",
			LastError:         item.Error,
			ExternalUpdatedAt: extUpdated,
			LastSyncAt:        now,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record 易协作 push")
			return
		}
	}
	if _, err := h.Queries.TouchYixiezuoConnectionPush(r.Context(), db.TouchYixiezuoConnectionPushParams{
		ID: conn.ID, WorkspaceID: wsUUID,
	}); err != nil {
		slog.Warn("touch yixiezuo push failed", append(logger.RequestAttrs(r), "error", err)...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var errYixiezuoSkip = errors.New("skip yixiezuo card")

func (h *Handler) applyYixiezuoCard(
	ctx context.Context,
	conn db.YixiezuoConnection,
	card yixiezuo.Card,
	statusMap map[string]string,
	catalogKeys, catalogNames []string,
	userID string,
) error {
	link, err := h.Queries.GetYixiezuoCardLinkByExternal(ctx, db.GetYixiezuoCardLinkByExternalParams{
		ConnectionID: conn.ID, ExternalIssueID: card.ExternalID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return h.createIssueFromYixiezuoCard(ctx, conn, card, statusMap, catalogKeys, catalogNames, userID)
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: link.IssueID, WorkspaceID: conn.WorkspaceID,
	})
	if err != nil {
		return err
	}
	if !yixiezuo.PreferRemote(card.UpdatedAt, issue.UpdatedAt.Time, link.Dirty) {
		return errYixiezuoSkip
	}
	fallbackStatus := issue.Status
	if fallbackStatus == "" {
		fallbackStatus = "todo"
	}
	status := yixiezuo.MapIncomingStatus(card.StatusName, statusMap, catalogKeys, catalogNames, fallbackStatus)
	if _, resolveErr := issuestatus.Resolve(ctx, h.Queries, conn.WorkspaceID, status); resolveErr != nil {
		status = fallbackStatus
	}
	priority := yixiezuo.MapIncomingPriority(card.Priority, issue.Priority)
	params := db.UpdateIssueParams{
		ID:            issue.ID,
		AssigneeType:  issue.AssigneeType,
		AssigneeID:    issue.AssigneeID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
		Stage:         issue.Stage,
		Title:         pgtype.Text{String: card.Title, Valid: true},
		Description:   pgtype.Text{String: card.Description, Valid: true},
		Status:        pgtype.Text{String: status, Valid: true},
		Priority:      pgtype.Text{String: priority, Valid: true},
	}
	if d, ok := parseYixiezuoDate(card.StartDate); ok {
		params.StartDate = d
	}
	if d, ok := parseYixiezuoDate(card.DueDate); ok {
		params.DueDate = d
	}
	updated, err := h.Queries.UpdateIssue(ctx, params)
	if err != nil {
		return err
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	extUpdated := pgtype.Timestamptz{}
	if !card.UpdatedAt.IsZero() {
		extUpdated = pgtype.Timestamptz{Time: card.UpdatedAt, Valid: true}
	}
	if _, err := h.Queries.UpsertYixiezuoCardLink(ctx, db.UpsertYixiezuoCardLinkParams{
		WorkspaceID:       conn.WorkspaceID,
		ConnectionID:      conn.ID,
		IssueID:           updated.ID,
		ExternalIssueID:   card.ExternalID,
		IssueRevision:     updated.Revision,
		Dirty:             false,
		LastDirection:     "pull",
		LastError:         "",
		ExternalUpdatedAt: extUpdated,
		LastSyncAt:        now,
	}); err != nil {
		return err
	}
	prefix := h.getIssuePrefix(ctx, updated.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, updated.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "member", userID, map[string]any{
		"issue":          resp,
		"status_changed": issue.Status != updated.Status,
		"title_changed":  issue.Title != updated.Title,
	})
	return nil
}

func (h *Handler) createIssueFromYixiezuoCard(
	ctx context.Context,
	conn db.YixiezuoConnection,
	card yixiezuo.Card,
	statusMap map[string]string,
	catalogKeys, catalogNames []string,
	userID string,
) error {
	status := yixiezuo.MapIncomingStatus(card.StatusName, statusMap, catalogKeys, catalogNames, "todo")
	if _, err := issuestatus.Resolve(ctx, h.Queries, conn.WorkspaceID, status); err != nil {
		status = "todo"
	}
	priority := yixiezuo.MapIncomingPriority(card.Priority, "medium")
	creatorID, err := util.ParseUUID(userID)
	if err != nil {
		return err
	}
	res, err := h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID:    conn.WorkspaceID,
		Title:          card.Title,
		Description:    pgtype.Text{String: card.Description, Valid: card.Description != ""},
		Status:         status,
		Priority:       priority,
		CreatorType:    "member",
		CreatorID:      creatorID,
		ProjectID:      conn.ProjectID,
		StartDate:      yixiezuoDateOrInvalid(card.StartDate),
		DueDate:        yixiezuoDateOrInvalid(card.DueDate),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{
		ActorID:  userID,
		Platform: "cli",
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
			payload := issueToResponse(issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
			h.fillStatusCategory(ctx, issue.WorkspaceID, &payload)
			return map[string]any{"issue": payload}
		},
	})
	if err != nil {
		return err
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	extUpdated := pgtype.Timestamptz{}
	if !card.UpdatedAt.IsZero() {
		extUpdated = pgtype.Timestamptz{Time: card.UpdatedAt, Valid: true}
	}
	_, err = h.Queries.UpsertYixiezuoCardLink(ctx, db.UpsertYixiezuoCardLinkParams{
		WorkspaceID:       conn.WorkspaceID,
		ConnectionID:      conn.ID,
		IssueID:           res.Issue.ID,
		ExternalIssueID:   card.ExternalID,
		IssueRevision:     res.Issue.Revision,
		Dirty:             false,
		LastDirection:     "pull",
		LastError:         "",
		ExternalUpdatedAt: extUpdated,
		LastSyncAt:        now,
	})
	return err
}

func (h *Handler) markYixiezuoDirty(ctx context.Context, issue db.Issue) {
	if err := h.Queries.MarkYixiezuoCardLinkDirtyByIssue(ctx, db.MarkYixiezuoCardLinkDirtyByIssueParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	}); err != nil {
		slog.Warn("mark yixiezuo dirty failed", "error", err, "issue_id", uuidToString(issue.ID))
	}
}

func (h *Handler) yixiezuoWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, db.Member, bool) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return pgtype.UUID{}, db.Member{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return pgtype.UUID{}, db.Member{}, false
	}
	return wsUUID, member, true
}

func (h *Handler) requireYixiezuoConnection(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID) (db.YixiezuoConnection, bool) {
	row, err := h.Queries.GetYixiezuoConnectionByWorkspace(r.Context(), wsUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "易协作 is not connected")
			return db.YixiezuoConnection{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load 易协作 connection")
		return db.YixiezuoConnection{}, false
	}
	return row, true
}

func (h *Handler) yixiezuoCatalog(ctx context.Context, workspaceID pgtype.UUID) ([]string, []string) {
	entries, err := h.Queries.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
		WorkspaceID: workspaceID, IncludeArchived: false,
	})
	if err != nil {
		return issuestatus.Canonical(), nil
	}
	keys := make([]string, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
		names = append(names, entry.Name)
	}
	return keys, names
}

func yixiezuoConnectionToResponse(row db.YixiezuoConnection) yixiezuoConnectionResponse {
	return yixiezuoConnectionResponse{
		ID:                uuidToString(row.ID),
		WorkspaceID:       uuidToString(row.WorkspaceID),
		ProjectID:         uuidToPtr(row.ProjectID),
		CLIBin:            row.CliBin,
		GCPHost:           row.GcpHost,
		ListQueryID:       row.ListQueryID,
		ExternalProjectID: row.ExternalProjectID,
		TrackerID:         row.TrackerID,
		StatusMap:         decodeYixiezuoStatusMap(row.StatusMap),
		LastPulledAt:      timestampToPtr(row.LastPulledAt),
		LastPushedAt:      timestampToPtr(row.LastPushedAt),
		CreatedAt:         timestampToString(row.CreatedAt),
		UpdatedAt:         timestampToString(row.UpdatedAt),
	}
}

func decodeYixiezuoStatusMap(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		return map[string]string{}
	}
	return out
}

func yixiezuoCardFromPayload(raw yixiezuoCardPayload) yixiezuo.Card {
	updated, _ := time.Parse(time.RFC3339, strings.TrimSpace(raw.UpdatedAt))
	if updated.IsZero() {
		updated, _ = time.Parse("2006-01-02 15:04:05", strings.TrimSpace(raw.UpdatedAt))
	}
	return yixiezuo.Card{
		ExternalID:  strings.TrimSpace(raw.ExternalID),
		Title:       strings.TrimSpace(raw.Title),
		Description: raw.Description,
		StatusName:  raw.StatusName,
		Priority:    raw.Priority,
		StartDate:   raw.StartDate,
		DueDate:     raw.DueDate,
		UpdatedAt:   updated,
	}
}

func yixiezuoExportFromIssue(issue db.Issue) yixiezuoExportIssue {
	return yixiezuoExportIssue{
		ID:          uuidToString(issue.ID),
		Title:       issue.Title,
		Description: stringOrEmpty(issue.Description),
		Status:      issue.Status,
		Priority:    issue.Priority,
		StartDate:   dateToPtr(issue.StartDate),
		DueDate:     dateToPtr(issue.DueDate),
		UpdatedAt:   timestampToString(issue.UpdatedAt),
		Revision:    issue.Revision,
	}
}

func parseYixiezuoDate(raw string) (pgtype.Date, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pgtype.Date{}, false
	}
	ts, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return pgtype.Date{}, false
	}
	return pgtype.Date{Time: ts, Valid: true}, true
}

func yixiezuoDateOrInvalid(raw string) pgtype.Date {
	if d, ok := parseYixiezuoDate(raw); ok {
		return d
	}
	return pgtype.Date{}
}

func stringOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
