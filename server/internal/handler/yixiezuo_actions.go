package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type yixiezuoActionError struct {
	status  int
	message string
}

func (e *yixiezuoActionError) Error() string       { return e.message }
func sourceError(status int, message string) error { return &yixiezuoActionError{status, message} }

func writeSourceError(w http.ResponseWriter, err error) {
	var action *yixiezuoActionError
	if errors.As(err, &action) {
		writeError(w, action.status, action.message)
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to process the source operation")
}

type yixiezuoIssueRequest struct {
	Summary      string `json:"summary"`
	StatusName   string `json:"status_name"`
	Revision     int64  `json:"revision"`
	Confirmed    bool   `json:"confirmed"`
	SourceDigest string `json:"source_digest"`
}

func sourceRequestKey(key string) pgtype.Text { return pgtype.Text{String: key, Valid: key != ""} }

func existingSourceRequest(ctx context.Context, q *db.Queries, ws, actor pgtype.UUID, key string) (db.YixiezuoOperation, bool, error) {
	if key == "" {
		return db.YixiezuoOperation{}, false, nil
	}
	row, err := q.GetYixiezuoOperationByRequestKey(ctx, db.GetYixiezuoOperationByRequestKeyParams{WorkspaceID: ws, RequestedBy: actor, RequestKey: sourceRequestKey(key)})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

func sourceScopeMatches(a, b *yixiezuo.ChannelScope) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// These actor-explicit actions are shared by HTTP and authenticated channel
// commands. Neither transport simulates HTTP requests for the other.
func (h *Handler) previewYixiezuo(ctx context.Context, ws, actor pgtype.UUID, rawURL, key string, scope *yixiezuo.ChannelScope) (db.YixiezuoOperation, error) {
	source, err := yixiezuo.ParseSource(rawURL)
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(400, err.Error())
	}
	payload, _ := json.Marshal(yixiezuo.OperationPayload{Source: source, Channel: scope})
	row, err := h.Queries.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, Kind: "preview", Payload: payload, RequestKey: sourceRequestKey(key)})
	if err != nil {
		return row, err
	}
	var saved yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &saved) != nil || row.Kind != "preview" || saved.Source != source || !sourceScopeMatches(saved.Channel, scope) {
		return row, sourceError(409, "this message already identifies a different source operation")
	}
	return row, nil
}

func (h *Handler) importYixiezuo(ctx context.Context, ws, actor, previewID, project pgtype.UUID, opts service.IssueCreateOpts) (db.Issue, bool, error) {
	row, err := h.Queries.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, ID: previewID})
	if err != nil || row.Kind != "preview" || row.State != "succeeded" {
		return db.Issue{}, false, sourceError(409, "read and review a source issue before importing")
	}
	var snapshot yixiezuo.Snapshot
	if json.Unmarshal(row.Result, &snapshot) != nil {
		return db.Issue{}, false, sourceError(500, "invalid source snapshot")
	}
	opts.ActorID, opts.Platform = uuidToString(actor), "yixiezuo"
	opts.BroadcastPayload = func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
		resp := issueToResponse(issue, h.getIssuePrefix(ctx, ws))
		h.fillStatusCategory(ctx, ws, &resp)
		return map[string]any{"issue": resp}
	}
	res, err := h.IssueService.Create(ctx, service.IssueCreateParams{WorkspaceID: ws, Title: snapshot.Title, Description: pgtype.Text{String: snapshot.Markdown(), Valid: true}, Status: "todo", Priority: yixiezuo.MapIncomingPriority(snapshot.Priority, "medium"), CreatorType: "member", CreatorID: actor, ProjectID: project, AllowDuplicate: true, Yixiezuo: &snapshot}, opts)
	if errors.Is(err, service.ErrYixiezuoAlreadyImported) {
		return *res.DuplicateIssue, true, h.attachSourceChat(ctx, *res.DuplicateIssue, opts.ChannelSource)
	}
	if errors.Is(err, service.ErrProjectNotFound) {
		return db.Issue{}, false, sourceError(400, "project not found in this workspace")
	}
	return res.Issue, false, err
}

func (h *Handler) attachSourceChat(ctx context.Context, issue db.Issue, src *service.ChannelIssueSource) error {
	if src == nil {
		return nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if err = q.LockYixiezuoImportSource(ctx, uuidToString(issue.WorkspaceID)+"/issue/"+uuidToString(issue.ID)); err != nil {
		return err
	}
	_, err = q.GetChannelIssueSourceByIssue(ctx, issue.ID)
	// A prior explicit association keeps its original notification route.
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = q.InsertChannelIssueSource(ctx, db.InsertChannelIssueSourceParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, InstallationID: src.InstallationID, ChannelType: src.ChannelType, ChannelChatID: src.ChatID, ChatType: src.ChatType, BindingID: src.BindingID, RouteRevision: src.RouteRevision})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) enqueueYixiezuo(ctx context.Context, ws, actor, id pgtype.UUID, kind string, req yixiezuoIssueRequest, key string, scope *yixiezuo.ChannelScope) (db.YixiezuoOperation, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.YixiezuoOperation{}, err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if err = q.LockYixiezuoImportSource(ctx, uuidToString(ws)+"/issue/"+uuidToString(id)); err != nil {
		return db.YixiezuoOperation{}, err
	}
	if prior, exists, err := existingSourceRequest(ctx, q, ws, actor, key); err != nil {
		return prior, err
	} else if exists {
		var saved yixiezuo.OperationPayload
		if json.Unmarshal(prior.Payload, &saved) != nil || prior.IssueID != id || prior.Kind != kind || !sourceScopeMatches(saved.Channel, scope) || saved.Summary != strings.TrimSpace(req.Summary) || saved.StatusName != req.StatusName {
			return prior, sourceError(409, "this message already identifies a different source operation")
		}
		return prior, nil
	}
	if err = q.ExpireYixiezuoOperations(ctx, ws); err != nil {
		return db.YixiezuoOperation{}, err
	}
	link, err := q.GetYixiezuoImportByIssue(ctx, db.GetYixiezuoImportByIssueParams{WorkspaceID: ws, IssueID: id})
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(404, "issue is not imported from 易协作")
	}
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(404, "issue not found")
	}
	var snapshot yixiezuo.Snapshot
	if json.Unmarshal(link.Snapshot, &snapshot) != nil {
		return db.YixiezuoOperation{}, sourceError(500, "invalid source snapshot")
	}
	if latest, err := q.LatestYixiezuoOperation(ctx, db.LatestYixiezuoOperationParams{WorkspaceID: ws, IssueID: id}); err == nil {
		if latest.State == "pending" || latest.State == "running" {
			return db.YixiezuoOperation{}, sourceError(409, "an operation is already pending for this issue")
		}
		if kind != "refresh" && (latest.State == "unknown" || latest.State == "conflict") {
			return db.YixiezuoOperation{}, sourceError(409, "refresh and review the remote source before publishing again")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.YixiezuoOperation{}, err
	}
	if kind == "review" {
		// Preparing a review freezes the current versions; it never queues a write.
		req.SourceDigest, req.Revision = snapshot.Digest, issue.Revision
	}
	if kind == "publish" || kind == "review" {
		if req.SourceDigest == "" || req.SourceDigest != snapshot.Digest {
			return db.YixiezuoOperation{}, sourceError(409, "the source preview changed; review it before publishing")
		}
		if (kind == "publish" && !req.Confirmed) || strings.TrimSpace(req.Summary) == "" || len(req.Summary) > 20000 {
			return db.YixiezuoOperation{}, sourceError(400, "review and confirm a result summary before publishing")
		}
		if req.Revision != issue.Revision {
			return db.YixiezuoOperation{}, sourceError(409, "the Multica issue changed; review its latest revision before publishing")
		}
		if snapshot.LockVersion == nil {
			return db.YixiezuoOperation{}, sourceError(409, "the source has no edit version; refresh it before publishing")
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
				return db.YixiezuoOperation{}, sourceError(400, "select a status from the source workflow")
			}
		}
	}
	payload, _ := json.Marshal(yixiezuo.OperationPayload{Source: snapshot.Source, ExpectedDigest: snapshot.Digest, Revision: issue.Revision, StatusName: req.StatusName, Summary: strings.TrimSpace(req.Summary), Channel: scope})
	var row db.YixiezuoOperation
	if kind == "review" {
		row, err = q.CreateYixiezuoReview(ctx, db.CreateYixiezuoReviewParams{WorkspaceID: ws, RequestedBy: actor, IssueID: id, Payload: payload, Result: link.Snapshot, RequestKey: sourceRequestKey(key)})
	} else {
		row, err = q.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, Kind: kind, IssueID: id, Payload: payload, RequestKey: sourceRequestKey(key)})
	}
	if err != nil {
		return row, err
	}
	return row, tx.Commit(ctx)
}
