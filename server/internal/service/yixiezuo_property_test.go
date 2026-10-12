package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestYixiezuoImportStoresPropertiesAndRefreshKeepsNativeFields(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	fx := testutil.New(pool, "", "")
	adaID := fx.User(t, "Ada Lovelace", "ada-"+suffix+"@multica.test")
	graceID := fx.User(t, "Grace Hopper", "grace-"+suffix+"@multica.test")
	workspaceID := fx.Workspace(t, "yx "+suffix, "yx-"+suffix)
	fx.Member(t, workspaceID, adaID, "owner")
	fx.Member(t, workspaceID, graceID, "member")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM yixiezuo_import WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue_property WHERE workspace_id=$1`, workspaceID)
	})

	ws := util.MustParseUUID(workspaceID)
	ada := util.MustParseUUID(adaID)
	svc := NewIssueService(db.New(pool), pool, nil, nil, nil)
	snapshot := yixiezuoSnapshot(t, "41", "Ready", []yixiezuo.SourceField{
		{Name: "版本", Value: "v1"},
		{Name: "相关链接", Value: "https://a.example/x、https://b.example/y"},
		{Name: "负责人", Value: "Ada Lovelace"},
		{Name: "参与者", Value: "Ada Lovelace"},
		{Name: "指派", Value: "Nobody"},
		{Name: "备注", Value: "alpha"},
		{Name: "开始日期", Value: "2026-04-01"},
	})
	snapshot.Description = "Repro"
	snapshot.Comments = []yixiezuo.Comment{{Author: "Ada", Content: "still open", CreatedAt: "2026-04-01"}}
	created, err := svc.Create(ctx, IssueCreateParams{
		WorkspaceID: ws, Title: snapshot.Title, Status: "todo", Priority: "medium",
		CreatorType: "member", CreatorID: ada, AllowDuplicate: true, Yixiezuo: &snapshot,
	}, IssueCreateOpts{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	issue := created.Issue
	if issue.Status != "todo" || !strings.Contains(issue.Description.String, "指派: Nobody") || !strings.Contains(issue.Description.String, "still open") || strings.Contains(issue.Description.String, "版本:") {
		t.Fatalf("imported issue = status %s description %q", issue.Status, issue.Description.String)
	}
	if got := dateText(issue.StartDate); got != "2026-04-01" || issue.DueDate.Valid {
		t.Fatalf("imported dates = %s valid-due %v", got, issue.DueDate.Valid)
	}
	assertYixiezuoProperties(t, pool, workspaceID, issue, map[string]string{
		"易协作链接": "https://dj01.pm.netease.com/issues/41",
		"版本":    "v1",
		"相关链接":  "https://a.example/x",
		"备注":    "alpha",
	})
	var actor, participants, tracked string
	fx.QueryRow(t, `
		SELECT issue.properties -> prop.id::text #>> '{}'
		FROM issue JOIN issue_property prop ON prop.workspace_id = issue.workspace_id AND prop.name = '负责人' AND prop.type = 'actor'
		WHERE issue.id = $1
	`, util.UUIDToString(issue.ID)).Scan(&actor)
	fx.QueryRow(t, `
		SELECT issue.properties -> prop.id::text ->> 0
		FROM issue JOIN issue_property prop ON prop.workspace_id = issue.workspace_id AND prop.name = '参与者' AND prop.type = 'multi_actor'
		WHERE issue.id = $1
	`, util.UUIDToString(issue.ID)).Scan(&participants)
	fx.QueryRow(t, `SELECT metadata->>'yixiezuo_status' FROM issue WHERE id=$1`, util.UUIDToString(issue.ID)).Scan(&tracked)
	if actor != "member:"+adaID || participants != "member:"+adaID || tracked != "Ready" {
		t.Fatalf("actor %s participants %s status %s", actor, participants, tracked)
	}

	fx.Exec(t, `
		UPDATE issue
		SET title = 'Local title', description = 'Local investigation', status = 'in_progress',
		    assignee_type = 'member', assignee_id = $2, due_date = '2026-01-15', start_date = '2026-02-02'
		WHERE id = $1
	`, util.UUIDToString(issue.ID), graceID)
	localProperty := fx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": workspaceID,
		"name":         "本地备注",
		"type":         "text",
	})
	fx.Exec(t, `UPDATE issue SET properties = properties || jsonb_build_object($2::text, to_jsonb('keep local'::text)) WHERE id=$1`, util.UUIDToString(issue.ID), localProperty)

	again, err := svc.Create(ctx, IssueCreateParams{
		WorkspaceID: ws, Title: "Replacement title", Status: "todo", Priority: "high",
		CreatorType: "member", CreatorID: ada, AllowDuplicate: true,
		Yixiezuo: func() *yixiezuo.Snapshot {
			next := snapshot
			next.Title = "Replacement title"
			next.Fields = []yixiezuo.SourceField{{Name: "版本", Value: "should-not-apply"}}
			return &next
		}(),
	}, IssueCreateOpts{})
	if !errors.Is(err, ErrYixiezuoAlreadyImported) || again.DuplicateIssue == nil || again.DuplicateIssue.Title != "Local title" {
		t.Fatalf("re-import = %+v %v", again.DuplicateIssue, err)
	}

	refreshed := snapshot
	refreshed.Title = "Changed source title"
	refreshed.Description = "Changed source description"
	refreshed.Status = "QA"
	refreshed.Fields = []yixiezuo.SourceField{
		{Name: "版本", Value: "v2"},
		{Name: "相关链接", Value: "https://b.example/y"},
		{Name: "负责人", Value: "Grace Hopper"},
		{Name: "参与者", Value: "Ada Lovelace、Grace Hopper"},
		{Name: "指派", Value: "Nobody"},
		{Name: "开始日期", Value: "2026-05-02"},
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := svc.ApplyYixiezuoRefresh(ctx, tx, ws, issue.ID, snapshot, refreshed); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var title, description, status, assignee, due, start, localValue, version, owner string
	fx.QueryRow(t, `
		SELECT title, description, status, assignee_id::text, due_date::text, start_date::text,
		       properties -> $2::text #>> '{}'
		FROM issue WHERE id=$1
	`, util.UUIDToString(issue.ID), localProperty).Scan(&title, &description, &status, &assignee, &due, &start, &localValue)
	if title != "Local title" || description != "Local investigation" || status != "in_progress" || assignee != graceID || due != "2026-01-15" || localValue != "keep local" {
		t.Fatalf("native/local fields = title %q desc %q status %s assignee %s due %s local %q", title, description, status, assignee, due, localValue)
	}
	if start != "2026-05-02" {
		t.Fatalf("start date = %s, want the refreshed source date", start)
	}
	fx.QueryRow(t, `
		SELECT issue.properties -> prop.id::text ->> 0
		FROM issue JOIN issue_property prop ON prop.workspace_id = issue.workspace_id AND prop.name = '版本'
		WHERE issue.id = $1
	`, util.UUIDToString(issue.ID)).Scan(&version)
	fx.QueryRow(t, `
		SELECT issue.properties -> prop.id::text #>> '{}'
		FROM issue JOIN issue_property prop ON prop.workspace_id = issue.workspace_id AND prop.name = '负责人'
		WHERE issue.id = $1
	`, util.UUIDToString(issue.ID)).Scan(&owner)
	fx.QueryRow(t, `SELECT metadata->>'yixiezuo_status' FROM issue WHERE id=$1`, util.UUIDToString(issue.ID)).Scan(&tracked)
	if version != "v2" || owner != "member:"+graceID || tracked != "QA" {
		t.Fatalf("refreshed source fields = version %s owner %s status %s", version, owner, tracked)
	}
	var participantCount int
	fx.QueryRow(t, `SELECT jsonb_array_length(i.properties -> p.id::text) FROM issue i JOIN issue_property p ON p.workspace_id=i.workspace_id AND p.name='参与者' WHERE i.id=$1`, util.UUIDToString(issue.ID)).Scan(&participantCount)
	if participantCount != 2 {
		t.Fatalf("refreshed participants = %d, want 2", participantCount)
	}
	refreshed.Fields[3].Value = "Grace Hopper"
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := svc.ApplyYixiezuoRefresh(ctx, tx, ws, issue.ID, snapshot, refreshed); err != nil {
		t.Fatalf("refresh back to one participant: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fx.QueryRow(t, `SELECT jsonb_array_length(i.properties -> p.id::text) FROM issue i JOIN issue_property p ON p.workspace_id=i.workspace_id AND p.name='参与者' WHERE i.id=$1`, util.UUIDToString(issue.ID)).Scan(&participantCount)
	if participantCount != 1 {
		t.Fatalf("refreshed participants = %d, want 1", participantCount)
	}
	var noteCount int
	fx.QueryRow(t, `
		SELECT count(*) FROM issue
		WHERE id = $1 AND properties ? (
			SELECT id::text FROM issue_property WHERE workspace_id = $2 AND name = '备注'
		)
	`, util.UUIDToString(issue.ID), workspaceID).Scan(&noteCount)
	if noteCount != 0 {
		t.Fatal("refresh kept a source field the new snapshot dropped")
	}

	cleared := refreshed
	cleared.Fields = []yixiezuo.SourceField{{Name: "版本", Value: "v2"}}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := svc.ApplyYixiezuoRefresh(ctx, tx, ws, issue.ID, refreshed, cleared); err != nil {
		t.Fatalf("clear refresh: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var clearedStart pgtype.Date
	var clearedDue string
	fx.QueryRow(t, `SELECT start_date, due_date::text FROM issue WHERE id=$1`, util.UUIDToString(issue.ID)).Scan(&clearedStart, &clearedDue)
	if clearedStart.Valid || clearedDue != "2026-01-15" {
		t.Fatalf("cleared dates = start valid %v due %s", clearedStart.Valid, clearedDue)
	}
}

func TestYixiezuoImportRollsBackWhenPropertyRejected(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	fx := testutil.New(pool, "", "")
	userID := fx.User(t, "Ada Lovelace", "ada-rollback-"+suffix+"@multica.test")
	workspaceID := fx.Workspace(t, "yx rollback "+suffix, "yx-rb-"+suffix)
	fx.Member(t, workspaceID, userID, "owner")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM yixiezuo_import WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue_property WHERE workspace_id=$1`, workspaceID)
	})
	fx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": workspaceID,
		"name":         "版本",
		"type":         "multi_text",
		"archived_at":  testutil.Raw("now()"),
	})
	svc := NewIssueService(db.New(pool), pool, nil, nil, nil)
	snapshot := yixiezuoSnapshot(t, "42", "Ready", []yixiezuo.SourceField{
		{Name: "全新字段", Value: "created then rolled back"},
		{Name: "版本", Value: "v1"},
	})
	_, err := svc.Create(ctx, IssueCreateParams{
		WorkspaceID: util.MustParseUUID(workspaceID), Title: snapshot.Title, Status: "todo", Priority: "medium",
		CreatorType: "member", CreatorID: util.MustParseUUID(userID), AllowDuplicate: true, Yixiezuo: &snapshot,
	}, IssueCreateOpts{})
	var prop *IssuePropertyValidationError
	if !errors.As(err, &prop) || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("import error = %v", err)
	}
	if fx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, workspaceID) != 0 {
		t.Fatal("rejected import left an issue")
	}
	if fx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE workspace_id=$1`, workspaceID) != 0 {
		t.Fatal("rejected import left a source link")
	}
	if fx.Count(t, `SELECT count(*) FROM issue_property WHERE workspace_id=$1 AND name IN ('全新字段', '易协作链接')`, workspaceID) != 0 {
		t.Fatal("rejected import left property definitions")
	}
}

func yixiezuoSnapshot(t *testing.T, externalID, status string, fields []yixiezuo.SourceField) yixiezuo.Snapshot {
	t.Helper()
	source, err := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/" + externalID)
	if err != nil {
		t.Fatal(err)
	}
	return yixiezuo.Snapshot{
		Source: source, Title: "Source " + externalID, Description: "Steps", Status: status,
		Fields: fields, Attachments: []yixiezuo.Attachment{}, Comments: []yixiezuo.Comment{},
	}
}

func assertYixiezuoProperties(t *testing.T, pool *pgxpool.Pool, workspaceID string, issue db.Issue, want map[string]string) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT properties FROM issue WHERE id=$1`, util.UUIDToString(issue.ID)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bag); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, first := range want {
		var id string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM issue_property WHERE workspace_id=$1 AND name=$2`, workspaceID, name).Scan(&id); err != nil {
			t.Fatalf("property %s: %v", name, err)
		}
		var values []string
		if err := json.Unmarshal(bag[id], &values); err != nil {
			t.Fatalf("property %s value %s: %v", name, bag[id], err)
		}
		if len(values) == 0 || values[0] != first {
			t.Fatalf("property %s = %v, want first %s", name, values, first)
		}
	}
}

func dateText(day pgtype.Date) string {
	if !day.Valid {
		return ""
	}
	return day.Time.Format("2006-01-02")
}
