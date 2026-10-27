package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementIssueGateFixture struct {
	workspaceID   string
	userID        string
	agentID       string
	requirementID string
	chatSessionID string
	planTaskID    string
	source        yixiezuo.Source
	service       *IssueService
	fixture       *testutil.Fixture
}

func newRequirementIssueGateFixture(t *testing.T) requirementIssueGateFixture {
	t.Helper()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	workspaceID := fx.Workspace(t, "Requirement issue gate", "requirement-issue-gate-"+uuid.NewString())
	userID := fx.User(t, "Requirement issue gate user", uuid.NewString()+"@example.test")
	fx = testutil.New(pool, workspaceID, userID)
	fx.Member(t, workspaceID, userID, "owner")
	runtimeID := fx.Runtime(t, "Requirement issue gate runtime", testutil.Cols{"provider": "claude"})
	agentID := fx.Agent(t, "Requirement issue gate agent", runtimeID)
	chatSessionID := fx.ChatSession(t, agentID)
	source := yixiezuo.Source{
		Host: "dj01.pm.netease.com", ID: "94100",
		URL: "https://dj01.pm.netease.com/issues/94100",
	}
	requirementID := fx.Insert(t, "requirement", testutil.Cols{
		"workspace_id": workspaceID, "source_host": source.Host, "external_id": source.ID,
		"source_url": source.URL, "source_snapshot": testutil.Raw(`'{}'::jsonb`), "source_digest": "gate-fixture",
		"requester_id": userID, "agent_id": agentID, "chat_session_id": chatSessionID, "intake_state": "awaiting_approval",
	})
	planTaskID := fx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "chat_session_id": chatSessionID, "requirement_id": requirementID, "mode": "plan", "status": "running",
	})
	q := db.New(pool)
	bus := events.New()
	tasks := &TaskService{Queries: q, TxStarter: pool, Bus: bus}
	return requirementIssueGateFixture{
		workspaceID: workspaceID, userID: userID, agentID: agentID, requirementID: requirementID,
		chatSessionID: chatSessionID, planTaskID: planTaskID, source: source,
		service: NewIssueService(q, pool, bus, nil, tasks), fixture: fx,
	}
}

func (f requirementIssueGateFixture) createParams(title string) IssueCreateParams {
	return IssueCreateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID), Title: title,
		Status: "todo", Priority: "none", CreatorType: "member", CreatorID: util.MustParseUUID(f.userID), AllowDuplicate: true,
	}
}

func assertRequirementApprovalGate(t *testing.T, err error) {
	t.Helper()
	var gateErr *RequirementChatExecutionError
	if !errors.As(err, &gateErr) || gateErr.Code != RequirementApprovalRequiredCode {
		t.Fatalf("create error = %v, want %s", err, RequirementApprovalRequiredCode)
	}
}

func TestGateRequirementIssueCreationCoversOriginAndSourcePaths(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		originType string
		originID   string
		agent      bool
	}{
		{name: "ordinary origin", originType: "requirement_chat", originID: f.chatSessionID},
		{name: "agent task origin", originType: "agent_create", originID: f.planTaskID, agent: true},
		{name: "quick create task origin", originType: "quick_create", originID: f.planTaskID, agent: true},
		{name: "channel origin", originType: "popo_chat", originID: f.chatSessionID},
		{name: "autopilot origin", originType: "autopilot", originID: f.chatSessionID, agent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := f.createParams("Requirement gate " + tc.name)
			params.OriginType = pgtype.Text{String: tc.originType, Valid: true}
			params.OriginID = requirementTestUUID(t, tc.originID)
			if tc.agent {
				params.CreatorType, params.CreatorID = "agent", requirementTestUUID(t, f.agentID)
			}
			_, err := f.service.Create(ctx, params, IssueCreateOpts{})
			assertRequirementApprovalGate(t, err)
		})
	}
	t.Run("source import", func(t *testing.T) {
		params := f.createParams("Requirement gate source import")
		params.Yixiezuo = &yixiezuo.Snapshot{Source: f.source, Title: "Source import", Description: "source", Status: "open"}
		_, err := f.service.Create(ctx, params, IssueCreateOpts{})
		assertRequirementApprovalGate(t, err)
	})
	if got := f.fixture.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, f.workspaceID); got != 0 {
		t.Fatalf("gate created issues = %d", got)
	}
}

func TestGateRequirementIssueCreationCoversPMNewIssue(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	projectID := f.fixture.Project(t, "Requirement PM gate")
	_, err := f.service.SyncYixiezuoPM(context.Background(), PMSyncParams{
		WorkspaceID: requirementTestUUID(t, f.workspaceID), ProjectID: requirementTestUUID(t, projectID),
		MemberID: requirementTestUUID(t, f.userID), ActorID: requirementTestUUID(t, f.userID), ActorType: "member",
		Scope: yixiezuo.PMScope{
			SourceHost: f.source.Host, SourceProject: "dj01", AssigneeEmails: []string{"owner@example.test"},
			IssueType: "BUG", ProjectID: projectID,
		},
		Source: yixiezuo.PMIssue{
			Source: f.source, Title: "Requirement PM gate", Description: "source", Status: "新建", Priority: "高",
			Assignee: yixiezuo.PMPerson{Email: "owner@example.test"}, Project: yixiezuo.PMProject{ID: "10", Name: "DJ01"},
			IssueType: "BUG", ObservedAt: time.Now().UTC(),
		},
	})
	assertRequirementApprovalGate(t, err)
	if got := f.fixture.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND project_id = $2`, f.workspaceID, projectID); got != 0 {
		t.Fatalf("PM gate created issues = %d", got)
	}
}

func TestGateRequirementIssueCreationLeavesOrdinaryAssignedCreateUnchanged(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	params := f.createParams("Ordinary assigned create without requirement")
	params.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	params.AssigneeID = requirementTestUUID(t, f.agentID)
	created, err := f.service.Create(context.Background(), params, IssueCreateOpts{})
	if err != nil {
		t.Fatalf("ordinary create: %v", err)
	}
	f.fixture.Cleanup(t, `DELETE FROM issue WHERE id = $1`, created.Issue.ID)
	f.fixture.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.Issue.ID)
	if !created.AssignedTaskID.Valid {
		t.Fatal("ordinary assigned create lost its run")
	}
	var mode, status string
	var requirementID *string
	f.fixture.QueryRow(t, `SELECT mode, status, requirement_id::text FROM agent_task_queue WHERE id = $1`, created.AssignedTaskID).Scan(&mode, &status, &requirementID)
	if mode != "execute" || status != "queued" || requirementID != nil {
		t.Fatalf("ordinary run = mode=%s status=%s requirement=%v", mode, status, requirementID)
	}
}

func TestGateRequirementIssueCreationReportsExistingAndCancelledRequirement(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	issueID := f.fixture.Issue(t, "Existing approved issue")
	f.fixture.Exec(t, `UPDATE requirement SET issue_id = $2 WHERE id = $1`, f.requirementID, issueID)
	params := f.createParams("Requirement gate existing")
	params.OriginType = pgtype.Text{String: "requirement_chat", Valid: true}
	params.OriginID = requirementTestUUID(t, f.chatSessionID)
	_, err := f.service.Create(context.Background(), params, IssueCreateOpts{})
	var gateErr *RequirementChatExecutionError
	if !errors.As(err, &gateErr) || gateErr.Code != RequirementChatIssueExistsCode || gateErr.IssueID != issueID {
		t.Fatalf("existing requirement error = %+v", gateErr)
	}
	f.fixture.Exec(t, `UPDATE requirement SET issue_id = NULL, cancelled_at = now() WHERE id = $1`, f.requirementID)
	_, err = f.service.Create(context.Background(), params, IssueCreateOpts{})
	if !errors.As(err, &gateErr) || gateErr.Code != RequirementCancelledCode {
		t.Fatalf("cancelled requirement error = %v", err)
	}
}

func TestGateRequirementIssueSourceCreationIgnoresUnrelatedSource(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	other := f.source
	other.ID = "94101"
	other.URL = "https://dj01.pm.netease.com/issues/94101"
	if err := GateRequirementIssueSourceCreation(context.Background(), db.New(sharedTestPool(t)), util.MustParseUUID(f.workspaceID), other); err != nil {
		t.Fatalf("unrelated source was gated: %v", err)
	}
}

func TestGateRequirementIssueCreationAllowsAgentTaskWithoutChat(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	var runtimeID string
	f.fixture.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.agentID).Scan(&runtimeID)
	taskID := f.fixture.Task(t, f.agentID, testutil.Cols{"runtime_id": runtimeID, "status": "completed"})
	params := f.createParams("Agent create without chat")
	params.OriginType = pgtype.Text{String: "agent_create", Valid: true}
	params.OriginID = requirementTestUUID(t, taskID)
	params.CreatorType, params.CreatorID = "agent", requirementTestUUID(t, f.agentID)
	created, err := f.service.Create(context.Background(), params, IssueCreateOpts{})
	if err != nil {
		t.Fatalf("agent create without chat: %v", err)
	}
	f.fixture.Cleanup(t, `DELETE FROM issue WHERE id = $1`, created.Issue.ID)
	f.fixture.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.Issue.ID)
	if !created.Issue.ID.Valid || created.Issue.OriginID != requirementTestUUID(t, taskID) {
		t.Fatalf("agent create without chat origin = %+v", created.Issue)
	}
}
