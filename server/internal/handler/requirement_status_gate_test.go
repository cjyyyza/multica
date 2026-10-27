package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func updateRequirementIssueStatusForTest(t *testing.T, f requirementReviewFixture, status string) *testutil.Response {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+uuidToString(f.Issue.ID), map[string]any{"status": status}), "id", uuidToString(f.Issue.ID))
	return testutil.Call(t, testHandler.UpdateIssue, req)
}

func assertRequirementStatusGateResponse(t *testing.T, response *testutil.Response) {
	t.Helper()
	response.Want(http.StatusConflict)
	if response.Map()["code"] != service.RequirementStatusTransitionForbiddenCode {
		t.Fatalf("status gate response = %s", response.Body)
	}
}

// setRequirementIssueStatusForTest moves a fixture issue with raw SQL: the
// reviewed fixture starts in_review and the policy under test must not be the
// thing that establishes the starting state.
func setRequirementIssueStatusForTest(t *testing.T, f *requirementReviewFixture, status string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE issue SET status = $2, revision = revision + 1 WHERE id = $1`, f.Issue.ID, status)
	row, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Issue = row
}

func TestRequirementTaskTokenCannotUseGenericStatusWrites(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name    string
		initial string
		next    string
	}{
		{name: "start", initial: "todo", next: "in_progress"},
		{name: "review", initial: "in_progress", next: "in_review"},
		{name: "resume", initial: "blocked", next: "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementReviewFixture(t, "96030")
			setRequirementIssueStatusForTest(t, &f, tc.initial)
			issueTaskID := newRequirementBlockTask(t, f)
			req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+uuidToString(f.Issue.ID), map[string]any{"status": tc.next}), "id", uuidToString(f.Issue.ID))
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Agent-ID", f.AgentID)
			req.Header.Set("X-Task-ID", issueTaskID)
			response := testutil.Call(t, testHandler.UpdateIssue, req)
			assertRequirementStatusGateResponse(t, response)
			if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != tc.initial {
				t.Fatalf("generic task write changed status to %s", got)
			}
		})
	}
}

// TestRequirementIssueResponseCarriesRequirementID pins the additive
// requirement_id IssueDetail uses to open the Requirement directly.
func TestRequirementIssueResponseCarriesRequirementID(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96032")
	issueID := uuidToString(f.Issue.ID)

	var detail IssueResponse
	testutil.Call(t, testHandler.GetIssue, withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID)).
		Want(http.StatusOK).JSON(&detail)
	if detail.RequirementID == nil || *detail.RequirementID != f.Detail.ID {
		t.Fatalf("detail requirement_id = %v, want %s", detail.RequirementID, f.Detail.ID)
	}

	var list struct {
		Issues []IssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.ListIssues, newRequest(http.MethodGet, "/api/issues", nil)).Want(http.StatusOK).JSON(&list)
	found := false
	for _, row := range list.Issues {
		if row.ID != issueID {
			continue
		}
		found = true
		if row.RequirementID == nil || *row.RequirementID != f.Detail.ID {
			t.Fatalf("list requirement_id = %v, want %s", row.RequirementID, f.Detail.ID)
		}
	}
	if !found {
		t.Fatalf("requirement issue %s missing from list", issueID)
	}

	ordinaryID := dbfx.Issue(t, "Ordinary issue without requirement")
	var ordinary IssueResponse
	testutil.Call(t, testHandler.GetIssue, withURLParam(newRequest(http.MethodGet, "/api/issues/"+ordinaryID, nil), "id", ordinaryID)).
		Want(http.StatusOK).JSON(&ordinary)
	if ordinary.RequirementID != nil {
		t.Fatalf("ordinary requirement_id = %v, want nil", *ordinary.RequirementID)
	}
}

func TestRequirementDirectStatusWriteGate(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96020")
	dbfx.Exec(t, `INSERT INTO issue_status (workspace_id, key, name, category, color, is_system)
		VALUES ($1, 'blocked', 'Blocked', 'started', '#000000', true)
		ON CONFLICT (workspace_id, key) DO UPDATE SET category = 'started'`, testWorkspaceID)
	for _, status := range []string{"done", "cancelled", "blocked"} {
		t.Run(status, func(t *testing.T) {
			assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, status))
			batch := testutil.Call(t, testHandler.BatchUpdateIssues, newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
				"issue_ids": []string{uuidToString(f.Issue.ID)}, "updates": map[string]any{"status": status},
			}))
			assertRequirementStatusGateResponse(t, batch)
			move := testutil.Call(t, testHandler.MoveIssue, withURLParam(newRequest(http.MethodPost, "/api/issues/"+uuidToString(f.Issue.ID)+"/move", map[string]any{
				"before_id": nil, "after_id": nil, "status": status,
			}), "id", uuidToString(f.Issue.ID)))
			assertRequirementStatusGateResponse(t, move)
		})
	}
	t.Run("agent task token", func(t *testing.T) {
		req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+uuidToString(f.Issue.ID), map[string]any{"status": "done"}), "id", uuidToString(f.Issue.ID))
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", f.AgentID)
		req.Header.Set("X-Task-ID", f.InitialTaskID)
		assertRequirementStatusGateResponse(t, testutil.Call(t, testHandler.UpdateIssue, req))
	})
	for _, tc := range []struct{ key, category string }{{"custom_terminal_done", "done"}, {"custom_terminal_closed", "closed"}} {
		dbfx.Insert(t, "issue_status", testutil.Cols{
			"workspace_id": testWorkspaceID, "key": tc.key, "name": tc.key, "category": tc.category, "color": "#000000", "position": 1,
		})
		assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, tc.key))
	}
	if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
		t.Fatalf("forbidden writes changed status to %s", got)
	}
}

func TestRequirementReviewToExecutingOnlyUsesReject(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96029")
	for _, status := range []string{"in_progress", "todo", "backlog"} {
		t.Run(status, func(t *testing.T) {
			assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, status))
			batch := testutil.Call(t, testHandler.BatchUpdateIssues, newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
				"issue_ids": []string{uuidToString(f.Issue.ID)}, "updates": map[string]any{"status": status},
			}))
			assertRequirementStatusGateResponse(t, batch)
			move := testutil.Call(t, testHandler.MoveIssue, withURLParam(newRequest(http.MethodPost, "/api/issues/"+uuidToString(f.Issue.ID)+"/move", map[string]any{
				"before_id": nil, "after_id": nil, "status": status,
			}), "id", uuidToString(f.Issue.ID)))
			assertRequirementStatusGateResponse(t, move)
		})
	}
	for _, tc := range []struct{ key, category string }{{"custom_review_started", "started"}, {"custom_review_unstarted", "unstarted"}} {
		dbfx.Insert(t, "issue_status", testutil.Cols{
			"workspace_id": testWorkspaceID, "key": tc.key, "name": tc.key, "category": tc.category, "color": "#000000", "position": 1,
		})
		assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, tc.key))
	}
	t.Run("agent task token", func(t *testing.T) {
		req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+uuidToString(f.Issue.ID), map[string]any{"status": "in_progress"}), "id", uuidToString(f.Issue.ID))
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", f.AgentID)
		req.Header.Set("X-Task-ID", f.InitialTaskID)
		assertRequirementStatusGateResponse(t, testutil.Call(t, testHandler.UpdateIssue, req))
	})
	if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
		t.Fatalf("forbidden executing writes changed status to %s", got)
	}
	ordinaryID := dbfx.Issue(t, "Ordinary review transition", testutil.Cols{"status": "in_review"})
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+ordinaryID, map[string]any{
		"status": "in_progress",
	}), "id", ordinaryID)).Want(http.StatusOK)
}

func TestRequirementDoneCancelledReopenAndRecoveryGate(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("done cannot reopen", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96021")
		acceptRequirementForTest(t, f, "reopen-accept-96021")
		assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, "in_progress"))
	})
	t.Run("cancelled cannot reopen", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96022")
		testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
			service.CancelRequirementRequest{ExpectedIssueRevision: ptr(f.Issue.Revision)}, "reopen-cancel-96022")).Want(http.StatusOK)
		assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, "todo"))
	})
	t.Run("feature off does not bypass review transitions", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96023")
		withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
		_, err := service.UpdateIssueStatusWithRequirementGate(context.Background(), testHandler.Queries, db.UpdateIssueStatusParams{
			ID: f.Issue.ID, WorkspaceID: f.Issue.WorkspaceID, Status: "todo",
		})
		var gateErr *service.RequirementChatExecutionError
		if !errors.As(err, &gateErr) || gateErr.Code != service.RequirementStatusTransitionForbiddenCode {
			t.Fatalf("feature-off review transition error = %v", err)
		}
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("feature-off direct write changed status to %s", got)
		}
		assertRequirementStatusGateResponse(t, updateRequirementIssueStatusForTest(t, f, "done"))
	})
}

func TestRequirementPRAndP4AutomationFence(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	setHandlerTestPRMergeStatus(t, "done")
	t.Run("pull request auto complete", func(t *testing.T) {
		t.Setenv("GITHUB_WEBHOOK_SECRET", "requirement-pr-gate")
		f := newRequirementReviewFixture(t, "96024")
		const installationID int64 = 9602401
		ordinary := prAutoCompleteTestIssue(t, "ordinary PR gate control", installationID)
		firePRWebhook(t, "requirement-pr-gate", installationID, 1, "Closes "+issueToResponse(f.Issue, testHandler.getIssuePrefix(context.Background(), f.Issue.WorkspaceID)).Identifier, "", "fix/requirement", "merged")
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("PR automation moved requirement to %s", got)
		}
		firePRWebhook(t, "requirement-pr-gate", installationID, 2, "Closes "+ordinary.Identifier, "", "fix/ordinary", "merged")
		if got := issueStatusForTest(t, ordinary.ID); got != "done" {
			t.Fatalf("ordinary PR automation regressed to %s", got)
		}
	})
	t.Run("P4 auto advance", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96025")
		var cl P4ChangelistResponse
		testutil.Call(t, testHandler.LinkIssueP4Changelist, p4UserRequest(
			http.MethodPost, "/api/issues/"+uuidToString(f.Issue.ID)+"/p4-changelists",
			map[string]any{"number": 794901, "port": p4TestPort}, uuidToString(f.Issue.ID),
		)).Want(http.StatusCreated).JSON(&cl)
		var applied struct {
			Updated int `json:"updated"`
		}
		testutil.Call(t, testHandler.UpdateP4ChangelistStatuses, p4DaemonRequest(
			http.MethodPost, "/api/daemon/p4/changelists/status", map[string]any{
				"workspace_id": testWorkspaceID,
				"updates":      []map[string]any{{"id": cl.ID, "status": "submitted"}},
			}, testWorkspaceID, "p4-requirement-gate"),
		).Want(http.StatusOK).JSON(&applied)
		if applied.Updated != 1 {
			t.Fatalf("P4 status update = %+v", applied)
		}
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("P4 automation moved requirement to %s", got)
		}
	})
}

func TestRequirementWorkflowAndPMStatusFence(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("workflow remap skips requirement and preserves ordinary", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96026")
		projectID := dbfx.Project(t, "Requirement workflow gate")
		dbfx.Exec(t, `UPDATE issue SET project_id = $2 WHERE id = $1`, f.Issue.ID, projectID)
		ordinaryID := dbfx.Issue(t, "Ordinary workflow remap", testutil.Cols{"project_id": projectID, "status": "in_review"})
		changed, _, err := remapIssues(context.Background(), testHandler.Queries, f.Issue.WorkspaceID, []pgtype.UUID{parseUUID(projectID)}, workflowMappingPlan{
			Required: []StatusMappingRequirement{{StatusKey: "in_review", IssueCount: 2, SuggestedStatusKey: "done"}},
		}, map[string]string{"in_review": "done"})
		if err != nil {
			t.Fatal(err)
		}
		if len(changed) != 1 || changed[0].ID != parseUUID(ordinaryID) {
			t.Fatalf("workflow remap changed = %+v", changed)
		}
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("workflow remap moved requirement to %s", got)
		}
		if got := issueStatusForTest(t, ordinaryID); got != "done" {
			t.Fatalf("ordinary workflow remap = %s", got)
		}
	})
	t.Run("PM sync cannot close requirement", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96027")
		projectID := dbfx.Project(t, "Requirement PM close gate")
		_, err := testHandler.IssueService.SyncYixiezuoPM(context.Background(), service.PMSyncParams{
			WorkspaceID: f.Issue.WorkspaceID, ProjectID: parseUUID(projectID),
			MemberID: parseUUID(testUserID), ActorID: parseUUID(testUserID), ActorType: "member",
			Scope: yixiezuo.PMScope{
				SourceHost: f.Source.Source.Host, SourceProject: "dj01", AssigneeEmails: []string{handlerTestEmail},
				IssueType: "BUG", ProjectID: projectID,
			},
			Source: yixiezuo.PMIssue{
				Source: f.Source.Source, Title: "Requirement PM close", Description: "source", Status: "已关闭", Priority: "高",
				Assignee: yixiezuo.PMPerson{Email: handlerTestEmail}, Project: yixiezuo.PMProject{ID: "10", Name: "DJ01"},
				IssueType: "BUG", ObservedAt: time.Now().UTC(),
			},
		})
		var gateErr *service.RequirementChatExecutionError
		if !errors.As(err, &gateErr) || gateErr.Code != service.RequirementStatusTransitionForbiddenCode {
			t.Fatalf("PM close error = %v", err)
		}
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("PM sync moved requirement to %s", got)
		}
	})
}

func TestRequirementDeletePrecheckAndTombstone(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96028")
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.AgentID).Scan(&runtimeID)
	runningID := dbfx.Task(t, f.AgentID, testutil.Cols{
		"issue_id": f.Issue.ID, "runtime_id": runtimeID, "requirement_id": f.Detail.ID,
		"mode": "execute", "status": "running", "started_at": testutil.Raw("now()"),
	})
	ordinaryID := dbfx.Issue(t, "Ordinary delete batch control")
	for _, deleteCall := range []struct {
		name string
		call func() *testutil.Response
	}{
		{name: "single", call: func() *testutil.Response {
			return testutil.Call(t, testHandler.DeleteIssue, withURLParam(newRequest(http.MethodDelete, "/api/issues/"+uuidToString(f.Issue.ID), nil), "id", uuidToString(f.Issue.ID)))
		}},
		{name: "batch", call: func() *testutil.Response {
			return testutil.Call(t, testHandler.BatchDeleteIssues, newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{
				"issue_ids": []string{ordinaryID, uuidToString(f.Issue.ID)},
			}))
		}},
	} {
		t.Run(deleteCall.name, func(t *testing.T) {
			response := deleteCall.call()
			response.Want(http.StatusConflict)
			if response.Map()["code"] != service.RequirementIssueNotCancelledCode {
				t.Fatalf("delete response = %s", response.Body)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, f.Issue.ID); got != 1 {
				t.Fatal("uncancelled requirement issue was deleted")
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, ordinaryID); got != 1 {
				t.Fatal("ordinary issue was deleted by a rejected batch")
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'running'`, runningID); got != 1 {
				t.Fatal("delete rejection cancelled an active run")
			}
		})
	}
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
		service.CancelRequirementRequest{ExpectedIssueRevision: ptr(f.Issue.Revision)}, "delete-cancel-96028")).Want(http.StatusOK)
	testutil.Call(t, testHandler.DeleteIssue, withURLParam(newRequest(http.MethodDelete, "/api/issues/"+uuidToString(f.Issue.ID), nil), "id", uuidToString(f.Issue.ID))).Want(http.StatusNoContent)
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE id = $1 AND issue_id = $2`, f.Detail.ID, f.Issue.ID); got != 1 {
		t.Fatal("delete cleared the permanent requirement issue binding")
	}
	detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.Detail.ID)
	if !detail.IssueDeleted || detail.State != "cancelled" || string(detail.Issue) != "null" {
		t.Fatalf("delete tombstone detail = %+v", detail)
	}
	err := service.GateRequirementIssueCreation(context.Background(), testHandler.Queries, service.IssueCreateParams{
		WorkspaceID: f.Issue.WorkspaceID, Title: "second requirement issue", Status: "todo", Priority: "none",
		CreatorType: "member", CreatorID: parseUUID(testUserID), AllowDuplicate: true,
		OriginType: pgtype.Text{String: "requirement_chat", Valid: true}, OriginID: f.Issue.OriginID,
	}, service.IssueCreateOpts{})
	var gateErr *service.RequirementChatExecutionError
	if !errors.As(err, &gateErr) || gateErr.Code != service.RequirementCancelledCode {
		t.Fatalf("delete/recreate gate error = %v", err)
	}
}

func TestRequirementBlockedNoopKeepsWakeupArmed(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96033")
	setRequirementIssueStatusForTest(t, &f, "blocked")
	var wakeupID string
	dbfx.QueryRow(t, `INSERT INTO issue_wakeup(
		id,workspace_id,issue_id,agent_id,created_by,instruction,kind,mode,requirement_id,enabled
	) VALUES(gen_random_uuid(),$1,$2,$3,$4,'question','event','once',$5,true) RETURNING id`,
		f.Issue.WorkspaceID, f.Issue.ID, f.AgentID, testUserID, f.Detail.ID).Scan(&wakeupID)
	_, _, err := updateIssueStoppingWakeups(context.Background(), testHandler.Queries, db.UpdateIssueParams{
		ID: f.Issue.ID, Status: pgtype.Text{String: "blocked", Valid: true},
	})
	if err != nil {
		t.Fatalf("blocked no-op: %v", err)
	}
	var enabled bool
	var disabledAt any
	dbfx.QueryRow(t, `SELECT enabled,disabled_at FROM issue_wakeup WHERE id=$1`, wakeupID).Scan(&enabled, &disabledAt)
	if !enabled || disabledAt != nil {
		t.Fatalf("blocked no-op fenced wakeup: enabled=%v disabled_at=%v", enabled, disabledAt)
	}
}
