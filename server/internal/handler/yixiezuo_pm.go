package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type pmPageRequest struct {
	Scope  yixiezuo.PMScope `json:"scope"`
	Offset int32            `json:"offset"`
}

type pmActor struct {
	workspace, project, agent, member, actor, task pgtype.UUID
	actorType, originator                          string
}

// PM routes accept the existing workspace-bound task identity. They do not
// relax the member-only gate on preview/import/bridge/publication routes.
func (h *Handler) pmWorkspace(w http.ResponseWriter, r *http.Request, scope *yixiezuo.PMScope) (pmActor, bool) {
	var a pmActor
	wsID := chi.URLParam(r, "id")
	var ok bool
	if a.workspace, ok = parseUUIDOrBadRequest(w, wsID, "workspace_id"); !ok {
		return a, false
	}
	member, ok := h.workspaceMember(w, r, wsID)
	if !ok {
		return a, false
	}
	if err := scope.Normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return a, false
	}
	if a.project, ok = parseUUIDOrBadRequest(w, scope.ProjectID, "project_id"); !ok {
		return a, false
	}
	agentID, _ := scope.TriageAgentID()
	if a.agent, ok = parseUUIDOrBadRequest(w, agentID, "triage_mention agent id"); !ok {
		return a, false
	}
	if scope.CategoryPropertyID != "" {
		id, valid := parseUUIDOrBadRequest(w, scope.CategoryPropertyID, "category_property_id")
		if !valid {
			return a, false
		}
		def, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{WorkspaceID: a.workspace, ID: id})
		if err != nil || def.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "category property not found or archived in this workspace")
			return a, false
		}
	}
	for email, id := range scope.AssigneeMap {
		parsed, valid := parseUUIDOrBadRequest(w, id, "assignee_map member id")
		if !valid {
			return a, false
		}
		scope.AssigneeMap[email] = uuidToString(parsed)
	}
	if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{WorkspaceID: a.workspace, ID: a.project}); err != nil {
		writeError(w, http.StatusBadRequest, "target project not found in this workspace")
		return a, false
	}
	a.member = member.UserID
	actorType, actorID := h.resolveActor(r, uuidToString(member.UserID), wsID)
	a.actorType, a.actor = actorType, parseUUID(actorID)
	a.originator = h.invokeOriginatorFromRequest(r, actorType, actorID)
	a.task = h.commentSourceTaskID(r)
	return a, true
}

func decodePMBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PM request: "+err.Error())
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(w, http.StatusBadRequest, "PM request must contain exactly one JSON object")
		return false
	}
	return true
}

func (h *Handler) SyncYixiezuoPMIssue(w http.ResponseWriter, r *http.Request) {
	r = h.withWakeupActor(r)
	var req yixiezuo.PMSourceRequest
	if !decodePMBody(w, r, &req) {
		return
	}
	a, ok := h.pmWorkspace(w, r, &req.Scope)
	if !ok {
		return
	}
	out, err := h.IssueService.SyncYixiezuoPM(r.Context(), service.PMSyncParams{WorkspaceID: a.workspace, ProjectID: a.project,
		MemberID: a.member, ActorID: a.actor, ActorType: a.actorType, SourceTaskID: a.task, Scope: req.Scope, Source: req.Issue})
	if err != nil {
		writeJSON(w, http.StatusOK, yixiezuo.PMResult{ExternalID: req.Issue.Source.ID, State: "failed", Error: err.Error(), Changes: []yixiezuo.PMChange{}, Warnings: []string{}})
		return
	}
	if out.Issue.ID.Valid {
		ws, err := h.Queries.GetWorkspace(r.Context(), a.workspace)
		if err == nil {
			out.Result.Identifier = service.IssueIdentifier(issuePrefixForWorkspace(ws), out.Issue.Number)
			if h.cfg.AppURL != "" {
				out.Result.URL = strings.TrimRight(h.cfg.AppURL, "/") + "/" + url.PathEscape(ws.Slug) + "/issues/" + out.Result.IssueID
			}
		}
		if out.Result.URL == "" {
			out.Result.Warnings = append(out.Result.Warnings, "Multica 链接缺失：工作区或应用地址不可用")
		}
		if out.Result.State == "created" || out.Result.State == "updated" {
			resp := issueToResponse(out.Issue, h.getIssuePrefix(r.Context(), a.workspace))
			h.fillStatusCategory(r.Context(), a.workspace, &resp)
			event := protocol.EventIssueUpdated
			if out.Result.State == "created" {
				event = protocol.EventIssueCreated
			}
			h.publish(event, uuidToString(a.workspace), a.actorType, uuidToString(a.actor), map[string]any{"issue": resp})
		}
		if out.Basis != nil {
			h.publishEvidenceComment(r.Context(), out.Issue, *out.Basis, out.BasisRevision)
		}
		h.broadcastCancelledWakeups(r.Context(), a.workspace, out.CancelledWakeups)
	}
	writeJSON(w, http.StatusOK, out.Result)
}

func (h *Handler) ListYixiezuoPMReconcileSources(w http.ResponseWriter, r *http.Request) {
	var req pmPageRequest
	if !decodePMBody(w, r, &req) {
		return
	}
	a, ok := h.pmWorkspace(w, r, &req.Scope)
	if !ok {
		return
	}
	if req.Offset < 0 {
		writeError(w, 400, "offset must be nonnegative")
		return
	}
	terminal, err := issuestatus.ExpandCategories(r.Context(), h.Queries, a.workspace, []string{issuestatus.CategoryDone, issuestatus.CategoryClosed})
	if err != nil {
		writeError(w, 500, "read terminal statuses: "+err.Error())
		return
	}
	rows, err := h.Queries.ListYixiezuoPMReconcileIssues(r.Context(), db.ListYixiezuoPMReconcileIssuesParams{WorkspaceID: a.workspace, ProjectID: a.project,
		SourceHost: req.Scope.SourceHost, TerminalStatusKeys: terminal, PageLimit: 101, PageOffset: req.Offset})
	if err != nil {
		writeError(w, 500, "read reconciliation sources: "+err.Error())
		return
	}
	hasMore := len(rows) > 100
	if hasMore {
		rows = rows[:100]
	}
	sources := []yixiezuo.Source{}
	failures := []yixiezuo.PMResult{}
	for _, row := range rows {
		rawURL := row.SourceUrl
		if rawURL == "" {
			id, markerErr := yixiezuo.PMExternalID(row.Description.String)
			if markerErr != nil || id == "" {
				failures = append(failures, yixiezuo.PMResult{IssueID: uuidToString(row.ID), State: "failed", Error: "外部单号缺失或冲突，无法关闭对账"})
				continue
			}
			rawURL = "https://" + req.Scope.SourceHost + "/issues/" + id
		}
		source, err := yixiezuo.ParseSource(rawURL)
		if err != nil || source.Host != req.Scope.SourceHost {
			failures = append(failures, yixiezuo.PMResult{IssueID: uuidToString(row.ID), State: "failed", Error: "来源链接与配置实例不一致"})
			continue
		}
		sources = append(sources, source)
	}
	writeJSON(w, 200, map[string]any{"sources": sources, "errors": failures, "has_more": hasMore, "next_offset": req.Offset + int32(len(rows))})
}

func (h *Handler) ListYixiezuoPMTriageCandidates(w http.ResponseWriter, r *http.Request) {
	var req pmPageRequest
	if !decodePMBody(w, r, &req) {
		return
	}
	a, ok := h.pmWorkspace(w, r, &req.Scope)
	if !ok {
		return
	}
	if req.Offset < 0 {
		writeError(w, 400, "offset must be nonnegative")
		return
	}
	terminal, err := issuestatus.ExpandCategories(r.Context(), h.Queries, a.workspace, []string{issuestatus.CategoryDone, issuestatus.CategoryClosed})
	if err != nil {
		writeError(w, 500, "read terminal statuses: "+err.Error())
		return
	}
	rows, err := h.Queries.ListYixiezuoPMTriageIssues(r.Context(), db.ListYixiezuoPMTriageIssuesParams{WorkspaceID: a.workspace, ProjectID: a.project,
		BacklogStatusKeys: []string{"backlog"}, TerminalStatusKeys: terminal, CategoryPropertyID: req.Scope.CategoryPropertyID, PageLimit: 101, PageOffset: req.Offset})
	if err != nil {
		writeError(w, 500, "read triage candidates: "+err.Error())
		return
	}
	hasMore := len(rows) > 100
	if hasMore {
		rows = rows[:100]
	}
	issues := []map[string]string{}
	for _, issue := range rows {
		issues = append(issues, map[string]string{"id": uuidToString(issue.ID), "identifier": service.IssueIdentifier(h.getIssuePrefix(r.Context(), a.workspace), issue.Number)})
	}
	writeJSON(w, 200, map[string]any{"issues": issues, "has_more": hasMore, "next_offset": req.Offset + int32(len(rows))})
}

func (h *Handler) NotifyYixiezuoPMTriage(w http.ResponseWriter, r *http.Request) {
	r = h.withWakeupActor(r)
	var req pmPageRequest
	if !decodePMBody(w, r, &req) {
		return
	}
	a, ok := h.pmWorkspace(w, r, &req.Scope)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	result, err := h.preparePMTriage(r, a, req.Scope, id)
	if err != nil {
		result.State, result.Error = "failed", err.Error()
	}
	if result.State == "pending" {
		result, err = h.deliverPMTriage(r, a, req.Scope, id)
		if err != nil {
			result.State, result.Error = "failed", err.Error()
		}
	}
	writeJSON(w, 200, result)
}

func pmTriageLock(ctx context.Context, q *db.Queries, a pmActor, issueID pgtype.UUID) error {
	return q.LockYixiezuoImportSource(ctx, "pm-triage/"+uuidToString(a.workspace)+"/"+uuidToString(issueID)+"/"+uuidToString(a.agent))
}

func (h *Handler) pmTriageEligible(ctx context.Context, q *db.Queries, a pmActor, scope yixiezuo.PMScope, issue db.Issue) (bool, error) {
	if issue.ProjectID != a.project || issue.AssigneeID.Valid {
		return false, nil
	}
	status := issuestatus.Effective(ctx, q, a.workspace, issue.Status)
	if status == "done" || status == "cancelled" {
		return false, nil
	}
	if !issue.TriageState.Valid && issue.Status != "backlog" && !strings.Contains(issue.Description.String, "待分诊") {
		return false, nil
	}
	if scope.CategoryPropertyID != "" {
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(issue.Properties, &properties); err != nil {
			return false, err
		}
		if raw := string(properties[scope.CategoryPropertyID]); raw != "" && raw != "null" && raw != "[]" && raw != `""` {
			return false, nil
		}
	} else {
		labels, err := q.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{WorkspaceID: a.workspace, IssueID: issue.ID})
		if err != nil {
			return false, err
		}
		if len(labels) != 0 {
			return false, nil
		}
	}
	processed, err := q.YixiezuoPMHumanHandled(ctx, db.YixiezuoPMHumanHandledParams{WorkspaceID: a.workspace, IssueID: issue.ID})
	return !processed, err
}

func (h *Handler) preparePMTriage(r *http.Request, a pmActor, scope yixiezuo.PMScope, id pgtype.UUID) (yixiezuo.PMTriageResult, error) {
	ctx := r.Context()
	result := yixiezuo.PMTriageResult{IssueID: uuidToString(id)}
	tx, err := h.beginWakeupWrite(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if err := pmTriageLock(ctx, q, a, id); err != nil {
		return result, err
	}
	issue, err := q.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{WorkspaceID: a.workspace, ID: id})
	if err != nil {
		return result, fmt.Errorf("issue not found in this workspace")
	}
	result.Identifier = service.IssueIdentifier(h.getIssuePrefix(ctx, a.workspace), issue.Number)
	eligible, err := h.pmTriageEligible(ctx, q, a, scope, issue)
	if err != nil {
		return result, err
	}
	if !eligible {
		result.State, result.ReasonCode = "skipped", "not_unclassified_or_human_handled"
		return result, nil
	}
	// Evaluate invocation rights before exposing a previous notification. The
	// receipt must not become an existence oracle for a private target.
	_, targets := h.resolveMentionedAgentCommentTriggers(ctx, issue, util.ParseMentions(scope.TriageMention), a.actorType, uuidToString(a.actor), commentTriggerComputeOptions{AuthoringTaskID: a.task, OriginatorUserID: a.originator})
	for _, target := range targets {
		if target.ReasonCode == ReasonInvocationNotAllowed {
			result.State, result.ReasonCode, result.Error = "failed", string(target.ReasonCode), string(target.ReasonCode)
			return result, nil
		}
	}
	target, err := q.GetAgent(ctx, a.agent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if err != nil || target.WorkspaceID != a.workspace {
		result.State, result.ReasonCode, result.Error = "failed", string(ReasonTargetUnavailable), string(ReasonTargetUnavailable)
		return result, nil
	}
	notification, err := q.GetYixiezuoPMNotification(ctx, db.GetYixiezuoPMNotificationParams{WorkspaceID: a.workspace, IssueID: id, AgentID: a.agent})
	if err == nil {
		result.CommentID = uuidToString(notification.CommentID)
		result.State = "pending"
		if notification.State == "notified" {
			result.State = "already_notified"
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if target.MaxConcurrentTasks != 1 {
		result.State, result.ReasonCode, result.Error = "failed", "triage_concurrency_must_be_one", "triage_concurrency_must_be_one"
		return result, nil
	}
	// Source titles are plain summary text, not additional executable mentions.
	title := strings.NewReplacer("[", "［", "]", "］", "\n", " ", "\r", " ").Replace(issue.Title)
	if runes := []rune(title); len(runes) > 240 {
		title = string(runes[:240]) + "…"
	}
	content := scope.TriageMention + " 这是一条未分类任务单，请分诊。摘要：" + title + "；状态：" + issue.Status + "；优先级：" + issue.Priority + "。"
	created, err := q.CreateComment(ctx, db.CreateCommentParams{ID: dbid.NewV7(), WorkspaceID: a.workspace, IssueID: id,
		AuthorType: a.actorType, AuthorID: a.actor, SourceTaskID: a.task, Type: "comment", Content: content})
	if err != nil {
		return result, fmt.Errorf("post triage comment: %w", err)
	}
	originator := pgtype.UUID{}
	if a.originator != "" {
		originator = parseUUID(a.originator)
	}
	if err := q.CreateYixiezuoPMNotification(ctx, db.CreateYixiezuoPMNotificationParams{WorkspaceID: a.workspace, IssueID: id, AgentID: a.agent, CommentID: created.ID, OriginatorUserID: originator}); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	h.publishEvidenceComment(ctx, issue, created.Comment(), created.IssueRevision)
	result.CommentID, result.State = uuidToString(created.ID), "pending"
	return result, nil
}

func (h *Handler) deliverPMTriage(r *http.Request, a pmActor, scope yixiezuo.PMScope, id pgtype.UUID) (yixiezuo.PMTriageResult, error) {
	ctx := r.Context()
	result := yixiezuo.PMTriageResult{IssueID: uuidToString(id)}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	// Hold only the advisory key during dispatch. Holding an issue or ledger
	// row here would invert the issue-delete / task-enqueue lock order.
	if err := pmTriageLock(ctx, q, a, id); err != nil {
		return result, err
	}
	n, err := q.GetYixiezuoPMNotification(ctx, db.GetYixiezuoPMNotificationParams{WorkspaceID: a.workspace, IssueID: id, AgentID: a.agent})
	if err != nil {
		return result, err
	}
	result.CommentID = uuidToString(n.CommentID)
	if n.State == "notified" {
		result.State = "already_notified"
		return result, nil
	}
	comment, err := q.GetComment(ctx, n.CommentID)
	if err != nil || comment.DeletedAt.Valid || comment.IssueID != id || comment.WorkspaceID != a.workspace {
		return result, fmt.Errorf("notification_comment_deleted: manual confirmation required")
	}
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: a.workspace, ID: id})
	if err != nil {
		return result, fmt.Errorf("issue not found in this workspace")
	}
	result.Identifier = service.IssueIdentifier(h.getIssuePrefix(ctx, a.workspace), issue.Number)
	covered, err := q.YixiezuoPMCommentHasTask(ctx, db.YixiezuoPMCommentHasTaskParams{WorkspaceID: a.workspace, IssueID: id, AgentID: a.agent, CommentID: n.CommentID})
	if err != nil {
		return result, err
	}
	outcome := CommentTriggerOutcome{TargetType: "agent", TargetID: uuidToString(a.agent), Status: DispatchQueued}
	if !covered {
		eligible, err := h.pmTriageEligible(ctx, q, a, scope, issue)
		if err != nil {
			return result, err
		}
		if !eligible {
			outcome.Status, outcome.ReasonCode = DispatchBlocked, "candidate_changed"
		}
		// Any authorized caller may retry, while the original comment retains
		// its author and invocation principal for permission and task attribution.
		originator := uuidToString(n.OriginatorUserID)
		_, targets := h.resolveMentionedAgentCommentTriggers(ctx, issue, util.ParseMentions(comment.Content), comment.AuthorType, uuidToString(comment.AuthorID), commentTriggerComputeOptions{AuthoringTaskID: comment.SourceTaskID, OriginatorUserID: originator})
		for _, t := range targets {
			if strings.EqualFold(t.TargetID, outcome.TargetID) && t.Status == DispatchBlocked {
				outcome.Status, outcome.ReasonCode = t.Status, t.ReasonCode
			}
		}
		if outcome.Status != DispatchBlocked {
			agent, lookupErr := q.GetAgent(ctx, a.agent)
			if lookupErr != nil {
				return result, lookupErr
			}
			if !h.canInvokeAgent(ctx, agent, a.actorType, uuidToString(a.actor), a.originator, uuidToString(a.workspace)) {
				outcome.Status, outcome.ReasonCode = DispatchBlocked, ReasonInvocationNotAllowed
			} else if agent.MaxConcurrentTasks != 1 {
				outcome.Status, outcome.ReasonCode = DispatchBlocked, "triage_concurrency_must_be_one"
			} else {
				outcomes := h.triggerTasksForComment(ctx, issue, comment, nil, comment.AuthorType, uuidToString(comment.AuthorID), originator, nil, commentSteerRequest{})
				outcome.Status, outcome.ReasonCode = DispatchBlocked, ReasonInternalError
				for _, o := range outcomes {
					if strings.EqualFold(o.TargetID, uuidToString(a.agent)) {
						outcome = o
					}
				}
			}
		}
	}
	result.State, result.ReasonCode = "notified", string(outcome.ReasonCode)
	if outcome.Status == DispatchBlocked {
		result.State, result.Error = "failed", string(outcome.ReasonCode)
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		return result, err
	}
	state := "notified"
	if result.State == "failed" {
		state = "failed"
	}
	if _, err := q.CompleteYixiezuoPMNotification(ctx, db.CompleteYixiezuoPMNotificationParams{WorkspaceID: a.workspace, IssueID: id, AgentID: a.agent, State: state, Receipt: raw, Error: result.Error}); err != nil {
		return result, fmt.Errorf("save triage receipt (comment %s): %w", result.CommentID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit triage receipt (comment %s): %w", result.CommentID, err)
	}
	return result, nil
}
