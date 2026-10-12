package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
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
	var prop *service.IssuePropertyValidationError
	if errors.As(err, &prop) {
		writeError(w, http.StatusBadRequest, prop.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to process the source operation")
}

type yixiezuoIssueRequest struct {
	Summary       string   `json:"summary"`
	StatusName    string   `json:"status_name"`
	Revision      int64    `json:"revision"`
	Confirmed     bool     `json:"confirmed"`
	SourceDigest  string   `json:"source_digest"`
	AttachmentIDs []string `json:"attachment_ids"`
	// attachmentUUIDs is filled at the HTTP boundary after AttachmentIDs is
	// validated. Channel confirms leave it empty.
	attachmentUUIDs []pgtype.UUID
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
	return h.queueYixiezuoPreview(ctx, ws, actor, rawURL, key, scope, yixiezuo.OperationPayload{})
}

func (h *Handler) queueYixiezuoPreview(ctx context.Context, ws, actor pgtype.UUID, rawURL, key string, scope *yixiezuo.ChannelScope, extra yixiezuo.OperationPayload) (db.YixiezuoOperation, error) {
	source, err := yixiezuo.ParseSource(rawURL)
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(400, err.Error())
	}
	extra.Source = source
	extra.Channel = scope
	payload, _ := json.Marshal(extra)
	row, err := h.Queries.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, Kind: "preview", Payload: payload, RequestKey: sourceRequestKey(key)})
	if err != nil {
		return row, err
	}
	var saved yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &saved) != nil || row.Kind != "preview" || saved.Source != source || !sourceScopeMatches(saved.Channel, scope) || saved.AutoImport != extra.AutoImport || saved.ProjectID != extra.ProjectID {
		return row, sourceError(409, "this message already identifies a different source operation")
	}
	return row, nil
}

func (h *Handler) expireYixiezuoOperations(ctx context.Context, ws pgtype.UUID) error {
	rows, err := h.Queries.ExpireYixiezuoOperations(ctx, ws)
	if err != nil {
		return err
	}
	h.settleExpiredYixiezuoAutoImports(ctx, rows)
	return nil
}

func (h *Handler) settleExpiredYixiezuoAutoImports(ctx context.Context, rows []db.YixiezuoOperation) {
	for _, row := range rows {
		h.settleYixiezuoAutoImport(ctx, row)
	}
}

func (h *Handler) settleYixiezuoAutoImport(ctx context.Context, row db.YixiezuoOperation) string {
	var payload yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &payload) != nil || !payload.AutoImport {
		return ""
	}
	text := h.autoImportResultText(ctx, row, payload)
	h.notifyYixiezuoAutoImport(ctx, row, payload, text)
	return text
}

func (h *Handler) autoImportResultText(ctx context.Context, row db.YixiezuoOperation, payload yixiezuo.OperationPayload) string {
	if row.State != "succeeded" {
		errText := strings.TrimSpace(row.Error)
		if errText == "" {
			errText = "the source could not be imported"
		}
		return fmt.Sprintf("Import failed for %s.\n%s", payload.Source.URL, errText)
	}
	project := pgtype.UUID{}
	if payload.ProjectID != "" {
		parsed, err := util.ParseUUID(payload.ProjectID)
		if err != nil || !parsed.Valid {
			return fmt.Sprintf("Import failed for %s.\nInvalid project ID.", payload.Source.URL)
		}
		project = parsed
	}
	opts := service.IssueCreateOpts{}
	if payload.Channel != nil {
		if install, err := util.ParseUUID(payload.Channel.InstallationID); err == nil && install.Valid {
			opts.ChannelSource = &service.ChannelIssueSource{
				InstallationID: install,
				ChannelType:    "popo",
				ChatID:         payload.Channel.ChatID,
				ChatType:       payload.Channel.ChatType,
			}
			opts.ChannelIdempotency = &service.ChannelIdempotencyKey{
				InstallationID: install,
				ChannelType:    "popo",
				MessageID:      "yixiezuo-auto-import:" + uuidToString(row.ID),
				Kind:           "source_import",
			}
		}
	}
	issue, existing, err := h.importYixiezuo(ctx, row.WorkspaceID, row.RequestedBy, row.ID, project, opts)
	if err != nil {
		return fmt.Sprintf("Import failed for %s.\n%s", payload.Source.URL, err.Error())
	}
	ident := uuidToString(issue.ID)
	if ws, wsErr := h.Queries.GetWorkspace(ctx, issue.WorkspaceID); wsErr == nil {
		ident = service.IssueIdentifier(issuePrefixForWorkspace(ws), issue.Number)
	}
	if existing {
		return "Already imported as " + ident + ". Existing edits, assignment and notification route are preserved.\n/status " + ident + "\n/reply " + ident + " <comment>"
	}
	return "Imported as " + ident + ". The task is unassigned; assign it in Multica when ready.\n/status " + ident + "\n/reply " + ident + " <comment>"
}

func (h *Handler) notifyYixiezuoAutoImport(ctx context.Context, row db.YixiezuoOperation, payload yixiezuo.OperationPayload, text string) {
	if h.PopoBridge == nil || payload.Channel == nil || strings.TrimSpace(text) == "" {
		return
	}
	install, err := util.ParseUUID(payload.Channel.InstallationID)
	if err != nil || !install.Valid {
		return
	}
	chatType := strings.TrimSpace(payload.Channel.ChatType)
	if chatType == "" {
		chatType = "p2p"
	}
	_ = h.PopoBridge.Enqueue(ctx, popo.OutboundItem{
		WorkspaceID:    row.WorkspaceID,
		InstallationID: install,
		ChatID:         payload.Channel.ChatID,
		ChatType:       chatType,
		Content:        text,
		SourceKey:      "yixiezuo-auto-import:" + uuidToString(row.ID),
		OutboundKind:   "issue_ack",
	})
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
	if err == nil {
		h.publishYixiezuoProperties(res.Issue, actor)
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
	expired, err := q.ExpireYixiezuoOperations(ctx, ws)
	if err != nil {
		return db.YixiezuoOperation{}, err
	}
	link, err := q.GetYixiezuoImportByIssue(ctx, db.GetYixiezuoImportByIssueParams{WorkspaceID: ws, IssueID: id})
	if err != nil {
		return db.YixiezuoOperation{}, sourceError(404, "issue is not imported from 易协作")
	}
	// Unbound uploads precede the issue lock, matching the regular issue-edit
	// path. Already issue-owned files are locked later, after their owner.
	if kind == "publish" && len(req.attachmentUUIDs) > 0 {
		if _, err := q.LockAttachmentsForIssueLink(ctx, db.LockAttachmentsForIssueLinkParams{
			WorkspaceID: ws, AttachmentIds: req.attachmentUUIDs,
		}); err != nil {
			return db.YixiezuoOperation{}, err
		}
	}
	var issue db.Issue
	if kind == "publish" {
		// Keep the reviewed version pinned through the evidence comment and
		// operation insert. Its revision must not absorb a concurrent edit.
		issue, err = q.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{WorkspaceID: ws, ID: id})
	} else {
		issue, err = q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: ws, ID: id})
	}
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
	// The evidence comment is part of this publication. Record the issue
	// revision after that comment so a successful receipt is not immediately
	// "local changes after publication" because of the comment we just wrote.
	recordedRevision := issue.Revision
	var evidence *db.Comment
	if kind == "publish" {
		comment, revision, err := h.recordPublishEvidence(ctx, q, ws, actor, id, strings.TrimSpace(req.Summary), req.attachmentUUIDs)
		if err != nil {
			return db.YixiezuoOperation{}, err
		}
		recordedRevision = revision
		evidence = &comment
	}
	payload, _ := json.Marshal(yixiezuo.OperationPayload{Source: snapshot.Source, ExpectedDigest: snapshot.Digest, Revision: recordedRevision, StatusName: req.StatusName, Summary: strings.TrimSpace(req.Summary), Channel: scope})
	var row db.YixiezuoOperation
	if kind == "review" {
		row, err = q.CreateYixiezuoReview(ctx, db.CreateYixiezuoReviewParams{WorkspaceID: ws, RequestedBy: actor, IssueID: id, Payload: payload, Result: link.Snapshot, RequestKey: sourceRequestKey(key)})
	} else {
		row, err = q.CreateYixiezuoOperation(ctx, db.CreateYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, Kind: kind, IssueID: id, Payload: payload, RequestKey: sourceRequestKey(key)})
	}
	if err != nil {
		return row, err
	}
	if err = tx.Commit(ctx); err != nil {
		return row, err
	}
	if evidence != nil {
		h.publishEvidenceComment(ctx, issue, *evidence, recordedRevision)
	}
	h.settleExpiredYixiezuoAutoImports(ctx, expired)
	return row, nil
}

// recordPublishEvidence writes the confirming member's result comment and
// binds the request's files to it before the external write is queued. The
// caller shares this transaction with the operation insert and rolls both
// back when binding fails.
func (h *Handler) recordPublishEvidence(ctx context.Context, q *db.Queries, ws, actor, issueID pgtype.UUID, summary string, attachmentIDs []pgtype.UUID) (db.Comment, int64, error) {
	reviews, err := q.ListSwarmReviewsByIssue(ctx, db.ListSwarmReviewsByIssueParams{IssueID: issueID, WorkspaceID: ws})
	if err != nil {
		return db.Comment{}, 0, err
	}
	created, err := q.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issueID,
		WorkspaceID: ws,
		AuthorType:  "member",
		AuthorID:    actor,
		Content:     publishEvidenceContent(summary, reviews),
		Type:        "comment",
	})
	if err != nil {
		return db.Comment{}, 0, err
	}
	if len(attachmentIDs) > 0 {
		if err = bindPublishEvidenceAttachments(ctx, q, ws, actor, issueID, created.ID, attachmentIDs); err != nil {
			return db.Comment{}, 0, err
		}
	}
	return created.Comment(), created.IssueRevision, nil
}

func publishEvidenceContent(summary string, reviews []db.ListSwarmReviewsByIssueRow) string {
	var body strings.Builder
	body.WriteString(strings.TrimSpace(summary))
	for _, review := range reviews {
		url := strings.TrimSpace(review.HtmlUrl)
		if url == "" {
			continue
		}
		body.WriteByte('\n')
		body.WriteString(url)
	}
	return body.String()
}

// bindPublishEvidenceAttachments consumes uploads locked before the issue,
// then follows POST /comments for issue-owned files. Any failed ownership or
// availability check rolls the entire publication back, including this link.
func bindPublishEvidenceAttachments(ctx context.Context, q *db.Queries, ws, actor, issueID, commentID pgtype.UUID, attachmentIDs []pgtype.UUID) error {
	if _, err := q.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		WorkspaceID: ws, IssueID: issueID, AttachmentIds: attachmentIDs, BumpRevision: false,
	}); err != nil {
		return err
	}
	missing, err := lockCommentAttachments(ctx, q, ws, issueID, attachmentIDs)
	if err != nil {
		return err
	}
	if missing.Valid {
		return sourceError(http.StatusConflict, "attachment "+uuidToString(missing)+" is no longer available")
	}
	actorID := uuidToString(actor)
	seen := make(map[pgtype.UUID]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		seen[id] = struct{}{}
		row, err := q.GetAttachment(ctx, db.GetAttachmentParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (row.UploaderType != "member" || uuidToString(row.UploaderID) != actorID || row.ChatSessionID.Valid || row.ChatMessageID.Valid || row.TaskID.Valid)) {
			return sourceError(http.StatusConflict, "attachment "+uuidToString(id)+" is no longer available")
		}
		if err != nil {
			return err
		}
	}
	if err = q.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{CommentID: commentID, IssueID: issueID, Column3: attachmentIDs}); err != nil {
		return err
	}
	linked, err := q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{CommentID: commentID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	if len(linked) != len(seen) {
		return sourceError(http.StatusConflict, "attachment is no longer available")
	}
	return nil
}

// The property event invalidates both issue value projections and the
// workspace catalog, including definitions created by a source import.
func (h *Handler) publishYixiezuoProperties(issue db.Issue, actor pgtype.UUID) {
	h.publish(protocol.EventIssuePropertiesChanged, uuidToString(issue.WorkspaceID), "member", uuidToString(actor), map[string]any{
		"issue_id":       uuidToString(issue.ID),
		"properties":     parseIssueProperties(issue.Properties),
		"issue_revision": issue.Revision,
	})
}

func (h *Handler) publishEvidenceComment(ctx context.Context, issue db.Issue, comment db.Comment, issueRevision int64) {
	atts, err := h.Queries.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{CommentID: comment.ID, WorkspaceID: issue.WorkspaceID})
	var responses []AttachmentResponse
	if err == nil {
		responses = make([]AttachmentResponse, 0, len(atts))
		for _, att := range atts {
			responses = append(responses, h.attachmentToResponse(att, attachmentURLModeSigned))
		}
	}
	resp := commentToResponse(comment, nil, responses)
	resp.IssueRevision = issueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), comment.AuthorType, uuidToString(comment.AuthorID), map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      issueRevision,
	})
}
