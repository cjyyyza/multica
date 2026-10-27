package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// requirementStatusGateFixture is the smallest real database state the status
// policy needs: one workspace, one issue and the requirement row that binds it.
type requirementStatusGateFixture struct {
	fixture       *testutil.Fixture
	queries       *db.Queries
	workspaceID   string
	userID        string
	agentID       string
	taskID        string
	issueID       string
	requirementID string
}

type requirementStatusGateFixtureOpts struct {
	status string
	// customStatus registers a catalog row for a non-built-in status key so the
	// policy resolves its category the way a real workspace does.
	customStatusKey      string
	customStatusCategory string
}

func newRequirementStatusGateFixture(t *testing.T, opts requirementStatusGateFixtureOpts) requirementStatusGateFixture {
	t.Helper()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	workspaceID := fx.Workspace(t, "Requirement status gate", "requirement-status-gate-"+uuid.NewString())
	userID := fx.User(t, "Requirement status gate user", uuid.NewString()+"@example.test")
	fx = testutil.New(pool, workspaceID, userID)
	fx.Member(t, workspaceID, userID, "owner")
	runtimeID := fx.Runtime(t, "Requirement status gate runtime", testutil.Cols{"provider": "claude"})
	agentID := fx.Agent(t, "Requirement status gate agent", runtimeID)
	if opts.customStatusKey != "" {
		fx.Insert(t, "issue_status", testutil.Cols{
			"workspace_id": workspaceID, "key": opts.customStatusKey,
			"name": opts.customStatusKey, "category": opts.customStatusCategory,
			"color": "#000000", "position": 1,
		})
	}
	status := opts.status
	if status == "" {
		status = "todo"
	}
	issueID := fx.Issue(t, "Requirement status gate issue", testutil.Cols{"status": status})
	requirementID := fx.Insert(t, "requirement", testutil.Cols{
		"workspace_id": workspaceID, "source_host": "dj01.pm.netease.com",
		"external_id": uuid.NewString(), "source_url": "https://dj01.pm.netease.com/issues/1",
		"source_snapshot": testutil.Raw(`'{}'::jsonb`), "source_digest": "status-gate",
		"requester_id": userID, "agent_id": agentID, "issue_id": issueID,
		"intake_state": "awaiting_approval",
	})
	taskID := fx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "requirement_id": requirementID,
		"mode": "execute", "status": "running",
	})
	return requirementStatusGateFixture{
		fixture: fx, queries: db.New(pool),
		workspaceID: workspaceID, userID: userID, agentID: agentID,
		taskID: taskID, issueID: issueID, requirementID: requirementID,
	}
}

func (f requirementStatusGateFixture) currentStatus(t *testing.T) string {
	t.Helper()
	issue, err := f.queries.GetIssue(context.Background(), requirementTestUUID(t, f.issueID))
	if err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	return issue.Status
}

func (f requirementStatusGateFixture) updateIssue(t *testing.T, status string, mutate func(*db.UpdateIssueParams)) (db.Issue, error) {
	t.Helper()
	params := db.UpdateIssueParams{
		ID:     requirementTestUUID(t, f.issueID),
		Status: pgtype.Text{String: status, Valid: true},
	}
	if mutate != nil {
		mutate(&params)
	}
	return UpdateIssueWithRequirementGate(context.Background(), f.queries, params)
}

func (f requirementStatusGateFixture) updateIssueStatus(t *testing.T, status string, mutate func(*db.UpdateIssueStatusParams)) (db.Issue, error) {
	t.Helper()
	params := db.UpdateIssueStatusParams{
		ID:          requirementTestUUID(t, f.issueID),
		WorkspaceID: requirementTestUUID(t, f.workspaceID),
		Status:      status,
	}
	if mutate != nil {
		mutate(&params)
	}
	return UpdateIssueStatusWithRequirementGate(context.Background(), f.queries, params)
}

func assertRequirementTransitionForbidden(t *testing.T, err error) {
	t.Helper()
	var gateErr *RequirementChatExecutionError
	if !errors.As(err, &gateErr) || gateErr.Code != RequirementStatusTransitionForbiddenCode {
		t.Fatalf("write error = %v, want %s", err, RequirementStatusTransitionForbiddenCode)
	}
}

// TestRequirementIssueStatusPolicyMatrix pins one row per allowed explicit
// action plus the generic-edit denials and the closed ELSE FALSE fallback.
func TestRequirementIssueStatusPolicyMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     string
		customKey   string
		customCat   string
		action      string
		next        string
		wantAllowed bool
	}{
		// Generic edits may not move a requirement issue.
		{name: "generic todo to in_progress", initial: "todo", next: "in_progress"},
		{name: "generic blocked to in_progress", initial: "blocked", next: "in_progress"},
		{name: "generic review to in_progress", initial: "in_review", next: "in_progress"},
		{name: "generic review to done", initial: "in_review", next: "done"},
		{name: "generic executing to cancelled", initial: "in_progress", next: "cancelled"},
		{name: "unknown action", initial: "todo", action: "made_up", next: "in_review"},
		{name: "action from wrong state", initial: "todo", action: RequirementIssueActionAccept, next: "done"},
		{name: "accept with wrong target", initial: "in_review", action: RequirementIssueActionAccept, next: "in_progress"},

		// Task start: within the executing state, never out of blocked.
		{name: "task start from todo", initial: "todo", action: RequirementIssueActionTaskStart, next: "in_progress", wantAllowed: true},
		{name: "task start from backlog", initial: "backlog", action: RequirementIssueActionTaskStart, next: "in_progress", wantAllowed: true},
		{name: "task start from blocked", initial: "blocked", action: RequirementIssueActionTaskStart, next: "in_progress"},
		{name: "task start from review", initial: "in_review", action: RequirementIssueActionTaskStart, next: "in_progress"},
		{name: "task start with wrong target", initial: "todo", action: RequirementIssueActionTaskStart, next: "in_review"},

		// Task completion.
		{name: "task complete from todo", initial: "todo", action: RequirementIssueActionTaskComplete, next: "in_review", wantAllowed: true},
		{name: "task complete from backlog", initial: "backlog", action: RequirementIssueActionTaskComplete, next: "in_review", wantAllowed: true},
		{name: "task complete from in_progress", initial: "in_progress", action: RequirementIssueActionTaskComplete, next: "in_review", wantAllowed: true},
		{name: "task complete from blocked", initial: "blocked", action: RequirementIssueActionTaskComplete, next: "in_review"},
		{name: "task complete from done", initial: "done", action: RequirementIssueActionTaskComplete, next: "in_review"},
		{name: "task complete with wrong target", initial: "todo", action: RequirementIssueActionTaskComplete, next: "done"},

		{name: "task reset from in progress", initial: "in_progress", action: RequirementIssueActionTaskReset, next: "todo", wantAllowed: true},
		{name: "task reset from blocked", initial: "blocked", action: RequirementIssueActionTaskReset, next: "todo"},
		{name: "task reset with wrong target", initial: "in_progress", action: RequirementIssueActionTaskReset, next: "in_review"},

		// Accept / reject.
		{name: "accept from review", initial: "in_review", action: RequirementIssueActionAccept, next: "done", wantAllowed: true},
		{name: "accept from cancelled", initial: "cancelled", action: RequirementIssueActionAccept, next: "done"},
		{name: "reject from review", initial: "in_review", action: RequirementIssueActionReject, next: "in_progress", wantAllowed: true},
		{name: "reject from todo", initial: "todo", action: RequirementIssueActionReject, next: "in_progress"},

		// Block / resume.
		{name: "block from in_progress", initial: "in_progress", action: RequirementIssueActionBlock, next: "blocked", wantAllowed: true},
		{name: "block from todo", initial: "todo", action: RequirementIssueActionBlock, next: "blocked", wantAllowed: true},
		{name: "block from review", initial: "in_review", action: RequirementIssueActionBlock, next: "blocked"},
		{name: "block from done", initial: "done", action: RequirementIssueActionBlock, next: "blocked"},
		{name: "resume from blocked", initial: "blocked", action: RequirementIssueActionWakeupResume, next: "in_progress", wantAllowed: true},
		{name: "resume from todo", initial: "todo", action: RequirementIssueActionWakeupResume, next: "in_progress"},

		// Cancel.
		{name: "cancel from review", initial: "in_review", action: RequirementIssueActionCancel, next: "cancelled", wantAllowed: true},
		{name: "cancel from todo", initial: "todo", action: RequirementIssueActionCancel, next: "cancelled", wantAllowed: true},
		{name: "cancel from done", initial: "done", action: RequirementIssueActionCancel, next: "cancelled"},

		// Custom terminal statuses fold to done/closed through the catalog.
		{name: "task complete from custom done", initial: "custom_done", customKey: "custom_done", customCat: "done", action: RequirementIssueActionTaskComplete, next: "in_review"},
		{name: "cancel from custom closed", initial: "custom_closed", customKey: "custom_closed", customCat: "closed", action: RequirementIssueActionCancel, next: "cancelled"},
		{name: "generic custom done same status", initial: "custom_done", customKey: "custom_done", customCat: "done", next: "custom_done", wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{
				status: tc.initial, customStatusKey: tc.customKey, customStatusCategory: tc.customCat,
			})
			_, err := f.updateIssue(t, tc.next, func(params *db.UpdateIssueParams) {
				if tc.action != "" {
					params.RequirementAction = RequirementIssueAction(tc.action)
				}
			})
			if !tc.wantAllowed {
				assertRequirementTransitionForbidden(t, err)
				if got := f.currentStatus(t); got != tc.initial {
					t.Fatalf("denied write changed status %s -> %s", tc.initial, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("allowed write failed: %v", err)
			}
			if got := f.currentStatus(t); got != tc.next {
				t.Fatalf("status = %s, want %s", got, tc.next)
			}
		})
	}
}

// TestRequirementIssueSameStatusWritesStayAllowed keeps field edits working on
// a requirement issue: an unchanged status is not a transition.
func TestRequirementIssueSameStatusWritesStayAllowed(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "in_progress"})
	updated, err := f.updateIssue(t, "in_progress", func(params *db.UpdateIssueParams) {
		params.Title = pgtype.Text{String: "Edited requirement issue", Valid: true}
	})
	if err != nil {
		t.Fatalf("title edit: %v", err)
	}
	if updated.Status != "in_progress" || updated.Title != "Edited requirement issue" {
		t.Fatalf("updated issue = status %s title %q", updated.Status, updated.Title)
	}

	updatedStatus, err := f.updateIssueStatus(t, "in_progress", nil)
	if err != nil {
		t.Fatalf("no-op status write: %v", err)
	}
	if updatedStatus.Status != "in_progress" || updatedStatus.Revision != updated.Revision {
		t.Fatalf("no-op status write = status %s revision %d, want in_progress revision %d",
			updatedStatus.Status, updatedStatus.Revision, updated.Revision)
	}
}

func TestRequirementIssueGenericTaskWritesStayDenied(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial string
		next    string
	}{
		{name: "ordinary task write cannot enter review", initial: "in_progress", next: "in_review"},
		{name: "ordinary task write cannot resume blocked", initial: "blocked", next: "in_progress"},
		{name: "ordinary task write cannot start", initial: "todo", next: "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: tc.initial})
			_, err := f.updateIssue(t, tc.next, func(params *db.UpdateIssueParams) {
				params.SourceTaskID = requirementTestUUID(t, f.taskID)
			})
			assertRequirementTransitionForbidden(t, err)
			if got := f.currentStatus(t); got != tc.initial {
				t.Fatalf("denied write changed status %s -> %s", tc.initial, got)
			}
		})
	}
}

// TestOrdinaryIssueStatusWritesStayUnrestricted is the control: the policy
// only governs issues a requirement binds.
func TestOrdinaryIssueStatusWritesStayUnrestricted(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "todo"})
	ordinaryID := f.fixture.Issue(t, "Ordinary issue next to a requirement", testutil.Cols{"status": "todo"})
	for _, next := range []string{"in_progress", "in_review", "blocked", "done", "cancelled"} {
		t.Run(next, func(t *testing.T) {
			updated, err := UpdateIssueWithRequirementGate(context.Background(), f.queries, db.UpdateIssueParams{
				ID: requirementTestUUID(t, ordinaryID), Status: pgtype.Text{String: next, Valid: true},
			})
			if err != nil {
				t.Fatalf("ordinary write to %s: %v", next, err)
			}
			if updated.Status != next {
				t.Fatalf("ordinary status = %s, want %s", updated.Status, next)
			}
		})
	}
}

// TestIssueToMapResolvedEmitsRequirementID pins the additive issue field used
// to open a Requirement without routing through the source chat session.
func TestIssueToMapResolvedEmitsRequirementID(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "todo"})
	issue, err := f.queries.GetIssue(context.Background(), requirementTestUUID(t, f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	payload := IssueToMapResolved(context.Background(), f.queries, issue, "REQ")
	if got, ok := payload["requirement_id"].(string); !ok || got != f.requirementID {
		t.Fatalf("requirement_id = %#v, want %s", payload["requirement_id"], f.requirementID)
	}

	ordinaryID := f.fixture.Issue(t, "Ordinary issue without requirement", testutil.Cols{"status": "todo"})
	ordinary, err := f.queries.GetIssue(context.Background(), requirementTestUUID(t, ordinaryID))
	if err != nil {
		t.Fatal(err)
	}
	ordinaryPayload := IssueToMapResolved(context.Background(), f.queries, ordinary, "REQ")
	if value, exists := ordinaryPayload["requirement_id"]; !exists || value != nil {
		t.Fatalf("ordinary requirement_id = %#v (exists %v), want nil", value, exists)
	}
}
