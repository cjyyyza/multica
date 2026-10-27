package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type PMSyncParams struct {
	WorkspaceID, ProjectID, MemberID, ActorID, SourceTaskID pgtype.UUID
	ActorType                                               string
	Scope                                                   yixiezuo.PMScope
	Source                                                  yixiezuo.PMIssue
}

type PMSyncOutcome struct {
	Issue            db.Issue
	Result           yixiezuo.PMResult
	Basis            *db.Comment
	BasisRevision    int64
	CancelledWakeups []db.AgentTaskQueue
}

type pmSyncState struct {
	SourceProject string           `json:"source_project"`
	Issue         yixiezuo.PMIssue `json:"issue"`
	AssigneeID    string           `json:"assignee_id"`
}

// SyncYixiezuoPM is a source-to-Multica transaction. It has no source driver
// and cannot enqueue a publication or invoke an agent by assigning an issue.
func (s *IssueService) SyncYixiezuoPM(ctx context.Context, p PMSyncParams) (PMSyncOutcome, error) {
	out := PMSyncOutcome{Result: yixiezuo.PMResult{ExternalID: p.Source.Source.ID, Changes: []yixiezuo.PMChange{}, Warnings: []string{}}}
	if err := p.Source.Validate(p.Scope.SourceHost); err != nil {
		return out, err
	}
	if strings.TrimSpace(p.Source.Status) == "关闭" {
		out.Result.Warnings = append(out.Result.Warnings, "来源状态为「关闭」，不在配置的排除名单内；本轮保留原始状态，请人工确认状态口径")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{WorkspaceID: p.WorkspaceID, ID: p.ProjectID}); err != nil {
		return out, fmt.Errorf("target project not found in this workspace")
	}
	if err := q.LockYixiezuoImportSource(ctx, util.UUIDToString(p.WorkspaceID)+"/"+p.Source.Source.Host+"/"+p.Source.Source.ID); err != nil {
		return out, err
	}
	// Share the ordinary create guard, including for terminal or Triage legacy
	// matches that the ordinary active-duplicate lookup intentionally excludes.
	if _, _, err := issueguard.LockAndFindActiveDuplicate(ctx, q, p.WorkspaceID, p.ProjectID, pgtype.UUID{}, p.Source.Title, true); err != nil {
		return out, err
	}
	link, linkErr := q.GetYixiezuoImportBySource(ctx, db.GetYixiezuoImportBySourceParams{WorkspaceID: p.WorkspaceID, SourceHost: p.Source.Source.Host, ExternalID: p.Source.Source.ID})
	if linkErr != nil && !errors.Is(linkErr, pgx.ErrNoRows) {
		return out, linkErr
	}
	var state pmSyncState
	if linkErr == nil && json.Unmarshal(link.PmState, &state) != nil {
		return out, fmt.Errorf("invalid saved PM sync state")
	}
	if !state.Issue.ObservedAt.IsZero() && p.Source.ObservedAt.Before(state.Issue.ObservedAt) {
		return out, fmt.Errorf("stale source observation; read this source issue again")
	}
	if state.Issue.UpdatedAt != "" && p.Source.UpdatedAt != "" {
		oldTime, oldErr := time.Parse(time.RFC3339Nano, state.Issue.UpdatedAt)
		newTime, newErr := time.Parse(time.RFC3339Nano, p.Source.UpdatedAt)
		if oldErr != nil || newErr != nil {
			return out, fmt.Errorf("invalid source update timestamp")
		}
		// Source list rows round timestamps to minutes, unlike detail reads.
		// Compare at the shared precision instead of rejecting newer scans.
		if state.Issue.UpdatedAtMinutePrecision || p.Source.UpdatedAtMinutePrecision {
			oldTime, newTime = oldTime.Truncate(time.Minute), newTime.Truncate(time.Minute)
		}
		if newTime.Before(oldTime) {
			return out, fmt.Errorf("source version is older than the last synchronized version")
		}
	}
	inScope := p.Scope.Includes(p.Source)
	terminal := yixiezuo.PMTerminalStatus(p.Source.Status)
	managed := linkErr == nil && state.Issue.Source.ID != ""
	if !inScope && terminal == "" {
		out.Result.State = "skipped"
		out.Result.Warnings = append(out.Result.Warnings, "来源工单不在配置的项目、BUG 类型或负责人范围内")
		return out, nil
	}
	var issue db.Issue
	if linkErr == nil {
		issue, err = q.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{WorkspaceID: p.WorkspaceID, ID: link.IssueID})
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fmt.Errorf("linked Multica issue no longer exists; manual confirmation required")
		}
	} else {
		issue, err = findPMLegacyIssue(ctx, q, p)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	found := err == nil
	// A known source binding in the configured destination also reconciles
	// older single imports after their source owner or BUG type has changed.
	if !inScope && !(terminal != "" && found && (managed || issue.ProjectID == p.ProjectID)) {
		out.Result.State = "skipped"
		out.Result.Warnings = append(out.Result.Warnings, "来源工单不在配置范围且没有对应的已同步任务单")
		return out, nil
	}
	if !found && yixiezuo.PMExcludedStatus(p.Source.Status) {
		out.Result.State = "skipped"
		return out, nil
	}
	if found {
		if err := yixiezuo.PMCheckDescriptionSource(issue.Description.String, p.Source.Source); err != nil {
			return out, err
		}
		other, otherErr := q.GetYixiezuoImportByIssue(ctx, db.GetYixiezuoImportByIssueParams{WorkspaceID: p.WorkspaceID, IssueID: issue.ID})
		if otherErr != nil && !errors.Is(otherErr, pgx.ErrNoRows) {
			return out, otherErr
		}
		if otherErr == nil && (other.SourceHost != p.Source.Source.Host || other.ExternalID != p.Source.Source.ID) {
			return out, fmt.Errorf("matching Multica issue is already bound to a different source issue")
		}
	}
	members, err := q.ListMembersWithUser(ctx, p.WorkspaceID)
	if err != nil {
		return out, fmt.Errorf("read workspace members: %w", err)
	}
	assignee, warning := pmMember(p.Source.Assignee, p.Scope.AssigneeMap, members)
	out.Result.SourceAssignee = strings.TrimSpace(p.Source.Assignee.Name + " " + p.Source.Assignee.Email)
	if warning != "" {
		out.Result.Warnings = append(out.Result.Warnings, warning)
	}
	assigneeType := pgtype.Text{}
	if assignee.Valid {
		assigneeType = pgtype.Text{String: "member", Valid: true}
	}
	priority := p.Source.Priority
	if mapped, ok := pmPriority(priority); ok {
		priority = mapped
	} else {
		priority = "none"
		if found {
			priority = issue.Priority
		}
		out.Result.Warnings = append(out.Result.Warnings, "易协作优先级缺失或无法映射："+yixiezuo.PMDisplay(p.Source.Priority))
	}
	closureStatusChanged := false
	if !found {
		if err := GateRequirementIssueSourceCreation(ctx, q, p.WorkspaceID, p.Source.Source); err != nil {
			return out, err
		}
		policy := ResolveIssueCountPolicy(ctx, s.Entitlements, p.WorkspaceID)
		number, err := AllocateIssueNumber(ctx, q, p.WorkspaceID, policy)
		if err != nil {
			return out, err
		}
		position, err := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, "backlog")
		if err != nil {
			return out, err
		}
		issue, err = q.CreateIssue(ctx, db.CreateIssueParams{ID: dbid.NewV7(), WorkspaceID: p.WorkspaceID, ProjectID: p.ProjectID,
			Title: p.Source.Title, Description: pgtype.Text{String: yixiezuo.PMDescription(p.Source), Valid: true},
			Status: "backlog", Priority: priority, AssigneeType: assigneeType, AssigneeID: assignee,
			CreatorType: p.ActorType, CreatorID: p.ActorID, Number: number, Position: position})
		if err != nil {
			return out, err
		}
		out.Result.State = "created"
	} else {
		out.Result.State = "unchanged"
		description, err := yixiezuo.PMAppendMarker(issue.Description.String, p.Source.Source.ID)
		if err != nil {
			return out, err
		}
		params := db.UpdateIssueParams{ID: issue.ID, SourceTaskID: p.SourceTaskID,
			AssigneeType: issue.AssigneeType, AssigneeID: issue.AssigneeID, ProjectID: issue.ProjectID,
			ParentIssueID: issue.ParentIssueID, StartDate: issue.StartDate, DueDate: issue.DueDate, Stage: issue.Stage, DuplicateOfIssueID: issue.DuplicateOfIssueID}
		change := func(field, old, next string) {
			if old != next {
				out.Result.Changes = append(out.Result.Changes, yixiezuo.PMChange{Field: field, Old: old, New: next})
			}
		}
		if inScope && !yixiezuo.PMExcludedStatus(p.Source.Status) {
			// Unchanged source fields do not undo a person's or the triager's
			// local edits. A newly resolvable mapping can fill an empty owner.
			if !managed || state.Issue.Title != p.Source.Title {
				change("标题", issue.Title, p.Source.Title)
				params.Title = pgtype.Text{String: p.Source.Title, Valid: true}
			}
			if !managed || state.Issue.Priority != p.Source.Priority {
				change("优先级", issue.Priority, priority)
				params.Priority = pgtype.Text{String: priority, Valid: true}
			}
			sourceOwnerChanged := state.Issue.Assignee.ID != p.Source.Assignee.ID || !strings.EqualFold(state.Issue.Assignee.Email, p.Source.Assignee.Email)
			mappingChanged := state.AssigneeID != util.UUIDToString(assignee)
			updateOwner := !managed && !issue.AssigneeID.Valid && assignee.Valid
			if managed {
				updateOwner = sourceOwnerChanged || (mappingChanged && (!issue.AssigneeID.Valid || (issue.AssigneeType.String == "member" && util.UUIDToString(issue.AssigneeID) == state.AssigneeID)))
			}
			if updateOwner {
				change("负责人", pmOwnerDisplay(issue.AssigneeType, issue.AssigneeID, members), pmAssigneeDisplay(assignee, members))
				params.AssigneeID, params.AssigneeType = assignee, assigneeType
			}
		}
		if description != issue.Description.String {
			change("外部单号标记", "缺失", p.Source.Source.ID)
			params.Description = pgtype.Text{String: description, Valid: true}
		}
		if terminal != "" {
			category := issuestatus.Effective(ctx, q, p.WorkspaceID, issue.Status)
			if category != "done" && category != "cancelled" {
				change("状态", issue.Status, terminal)
				params.Status = pgtype.Text{String: terminal, Valid: true}
			}
		}
		if len(out.Result.Changes) > 0 {
			issue, err = UpdateIssueWithRequirementGate(ctx, q, params)
			if err != nil {
				return out, err
			}
			if params.Status.Valid && issue.Status != "blocked" {
				if _, err := disableRequirementWakeupsForIssue(ctx, q, issue.WorkspaceID, issue.ID); err != nil {
					return out, err
				}
			}
			out.Result.State = "updated"
		}
		closureStatusChanged = params.Status.Valid
	}
	var snapshot yixiezuo.Snapshot
	if linkErr == nil {
		if err := json.Unmarshal(link.Snapshot, &snapshot); err != nil {
			return out, fmt.Errorf("invalid saved source snapshot")
		}
		if snapshot.Status != p.Source.Status {
			out.Result.Changes = append(out.Result.Changes, yixiezuo.PMChange{Field: "易协作状态", Old: snapshot.Status, New: p.Source.Status})
		}
		if snapshot.Priority != p.Source.Priority {
			out.Result.Changes = append(out.Result.Changes, yixiezuo.PMChange{Field: "易协作优先级", Old: snapshot.Priority, New: p.Source.Priority})
		}
	}
	snapshot.Source, snapshot.Title, snapshot.Status, snapshot.Priority, snapshot.UpdatedAt = p.Source.Source, p.Source.Title, p.Source.Status, p.Source.Priority, p.Source.UpdatedAt
	if linkErr != nil {
		snapshot.Description = p.Source.Description
	}
	if snapshot.Attachments == nil {
		snapshot.Attachments = []yixiezuo.Attachment{}
	}
	if snapshot.Comments == nil {
		snapshot.Comments = []yixiezuo.Comment{}
	}
	if snapshot.Statuses == nil {
		snapshot.Statuses = []yixiezuo.Status{}
	}
	if snapshot.Warnings == nil {
		snapshot.Warnings = []string{}
	}
	if snapshot.Fields == nil {
		snapshot.Fields = []yixiezuo.SourceField{}
	}
	snapshot.Digest = snapshot.Fingerprint()
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return out, err
	}
	if linkErr != nil {
		_, err = q.CreateYixiezuoImport(ctx, db.CreateYixiezuoImportParams{WorkspaceID: p.WorkspaceID, IssueID: issue.ID,
			SourceHost: p.Source.Source.Host, ExternalID: p.Source.Source.ID, SourceUrl: p.Source.Source.URL, Snapshot: rawSnapshot, ImportedBy: p.MemberID})
		if err != nil {
			return out, err
		}
	}
	state.SourceProject, state.Issue = p.Scope.SourceProject, p.Source
	state.AssigneeID = util.UUIDToString(assignee)
	rawState, err := json.Marshal(state)
	if err != nil {
		return out, err
	}
	if err := q.UpdateYixiezuoPMState(ctx, db.UpdateYixiezuoPMStateParams{WorkspaceID: p.WorkspaceID, IssueID: issue.ID, Snapshot: rawSnapshot, PmState: rawState}); err != nil {
		return out, err
	}
	for _, metadataField := range []struct{ key, field, value string }{
		{"yixiezuo_url", "来源链接", p.Source.Source.URL},
		{"yixiezuo_id", "来源关联", p.Source.Source.ID},
		{"yixiezuo_status", "易协作状态", p.Source.Status},
		{"yixiezuo_priority", "易协作优先级", p.Source.Priority},
	} {
		key, value := metadataField.key, metadataField.value
		raw, err := json.Marshal(value)
		if err != nil {
			return out, err
		}
		if _, err := q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{WorkspaceID: p.WorkspaceID, ID: issue.ID, Key: key, Value: raw}); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
		} else if found {
			field := metadataField.field
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
				return out, err
			}
			var old string
			if data := metadata[key]; len(data) > 0 {
				if err := json.Unmarshal(data, &old); err != nil {
					old = string(data)
				}
			}
			present := false
			for _, change := range out.Result.Changes {
				if change.Field == field {
					present = true
				}
			}
			if !present {
				out.Result.Changes = append(out.Result.Changes, yixiezuo.PMChange{Field: field, Old: old, New: value})
			}
		}
	}
	// Record closure evidence after metadata so both published events carry
	// the final issue revision from this transaction.
	if closureStatusChanged {
		created, err := q.CreateComment(ctx, db.CreateCommentParams{ID: dbid.NewV7(), WorkspaceID: p.WorkspaceID, IssueID: issue.ID,
			AuthorType: "system", AuthorID: p.MemberID, Type: "status_change", SourceTaskID: p.SourceTaskID,
			Content: fmt.Sprintf("依据易协作工单 #%s 的状态「%s」，将本单置为 %s。来源：%s", p.Source.Source.ID, p.Source.Status, terminal, p.Source.Source.URL)})
		if err != nil {
			return out, fmt.Errorf("record source closure evidence: %w", err)
		}
		comment := created.Comment()
		out.Basis, out.BasisRevision = &comment, created.IssueRevision
		out.CancelledWakeups, err = StopClosedIssueWakeups(ctx, q, issue)
		if err != nil {
			return out, err
		}
	}
	if len(out.Result.Changes) > 0 && out.Result.State == "unchanged" {
		out.Result.State = "updated"
	}
	if out.Result.State != "unchanged" {
		details, err := json.Marshal(map[string]any{"source": "yixiezuo_pm", "external_id": p.Source.Source.ID, "changes": out.Result.Changes})
		if err != nil {
			return out, err
		}
		if _, err := q.CreateActivity(ctx, db.CreateActivityParams{WorkspaceID: p.WorkspaceID, IssueID: issue.ID,
			ActorType: pgtype.Text{String: p.ActorType, Valid: true}, ActorID: p.ActorID, Action: "yixiezuo_synced", Details: details}); err != nil {
			return out, err
		}
	}
	issue, err = q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: p.WorkspaceID, ID: issue.ID})
	if err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Issue, out.Result.IssueID = issue, util.UUIDToString(issue.ID)
	out.Result.Assignee = pmOwnerDisplay(issue.AssigneeType, issue.AssigneeID, members)
	return out, nil
}

func findPMLegacyIssue(ctx context.Context, q *db.Queries, p PMSyncParams) (db.Issue, error) {
	pattern := `(^|\n)[[:blank:]]*外部单号[：:][[:blank:]]*` + p.Source.Source.ID + `[[:blank:]]*(\r?\n|$)`
	issues, err := q.FindYixiezuoPMMarkerIssues(ctx, db.FindYixiezuoPMMarkerIssuesParams{WorkspaceID: p.WorkspaceID, MarkerPattern: pattern})
	if err != nil {
		return db.Issue{}, err
	}
	if len(issues) == 0 {
		issues, err = q.FindYixiezuoPMTitleIssues(ctx, db.FindYixiezuoPMTitleIssuesParams{WorkspaceID: p.WorkspaceID, ProjectID: p.ProjectID, NormalizedTitle: issueguard.NormalizeTitle(p.Source.Title)})
		if err != nil {
			return db.Issue{}, err
		}
	}
	if len(issues) > 1 {
		return db.Issue{}, fmt.Errorf("multiple Multica issues match the source marker or project and title; manual confirmation required")
	}
	if len(issues) == 0 {
		return db.Issue{}, pgx.ErrNoRows
	}
	return issues[0], nil
}

func pmMember(person yixiezuo.PMPerson, mapping map[string]string, members []db.ListMembersWithUserRow) (pgtype.UUID, string) {
	email := strings.ToLower(strings.TrimSpace(person.Email))
	var matches []pgtype.UUID
	for _, member := range members {
		if id, configured := mapping[email]; configured {
			if util.UUIDToString(member.UserID) == id {
				matches = append(matches, member.UserID)
			}
		} else if email != "" {
			if strings.EqualFold(member.UserEmail, email) {
				matches = append(matches, member.UserID)
			}
		} else if person.Name != "" && member.UserName == person.Name {
			matches = append(matches, member.UserID)
		}
	}
	if len(matches) == 1 {
		return matches[0], ""
	}
	return pgtype.UUID{}, "来源负责人无法唯一映射到 Multica 成员：" + yixiezuo.PMDisplay(strings.TrimSpace(person.Name+" "+person.Email))
}

func pmOwnerDisplay(kind pgtype.Text, id pgtype.UUID, members []db.ListMembersWithUserRow) string {
	if id.Valid && kind.String != "member" {
		return kind.String + ": " + util.UUIDToString(id)
	}
	return pmAssigneeDisplay(id, members)
}

func pmAssigneeDisplay(id pgtype.UUID, members []db.ListMembersWithUserRow) string {
	if !id.Valid {
		return "未分配"
	}
	for _, member := range members {
		if member.UserID == id {
			return member.UserName + " (" + member.UserEmail + ")"
		}
	}
	return util.UUIDToString(id)
}

func pmPriority(value string) (string, bool) {
	const missing = "__unmapped__"
	mapped := yixiezuo.MapIncomingPriority(value, missing)
	return mapped, mapped != missing
}
