package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRequirementTerminalIssueExecutionAndTaskCreationFences(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		cancelled bool
		wantCode  string
	}{
		{name: "done", status: "done", wantCode: "requirement_done"},
		{name: "cancelled", status: "cancelled", cancelled: true, wantCode: RequirementCancelledCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementIssueGateFixture(t)
			issueID := f.fixture.Issue(t, "Terminal requirement "+tc.name, testutil.Cols{
				"status": tc.status, "assignee_type": "agent", "assignee_id": f.agentID,
			})
			f.fixture.Exec(t, `UPDATE requirement SET issue_id = $2, cancelled_at = CASE WHEN $3 THEN now() ELSE NULL END WHERE id = $1`, f.requirementID, issueID, tc.cancelled)
			f.fixture.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
			pool := sharedTestPool(t)
			q := db.New(pool)
			svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
			ctx := context.Background()
			rerun, err := svc.RerunIssue(ctx, util.MustParseUUID(issueID), pgtype.UUID{}, pgtype.UUID{}, util.MustParseUUID(f.userID), nil)
			var gateErr *RequirementChatExecutionError
			if !errors.As(err, &gateErr) || gateErr.Code != tc.wantCode {
				t.Errorf("rerun error = %v, want %s", err, tc.wantCode)
			}
			if rerun != nil && rerun.ID.Valid {
				f.fixture.Exec(t, `DELETE FROM agent_task_queue WHERE id = $1`, rerun.ID)
			}
			var runtimeID string
			f.fixture.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.agentID).Scan(&runtimeID)
			parentID := f.fixture.Task(t, f.agentID, testutil.Cols{
				"runtime_id": runtimeID, "issue_id": issueID, "requirement_id": f.requirementID,
				"mode": "execute", "status": "failed", "completed_at": testutil.Raw("now()"),
			})
			assertNoTask := func(name string, task db.AgentTaskQueue, err error) {
				t.Helper()
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Errorf("%s error = %v, want pgx.ErrNoRows", name, err)
				}
				if task.ID.Valid {
					f.fixture.Exec(t, `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
				}
			}
			task, err := q.CreateAgentTask(ctx, db.CreateAgentTaskParams{
				AgentID: util.MustParseUUID(f.agentID), RuntimeID: util.MustParseUUID(runtimeID), IssueID: util.MustParseUUID(issueID),
			})
			assertNoTask("CreateAgentTask", task, err)
			deferred, err := q.CreateDeferredChannelIssueTask(ctx, db.CreateDeferredChannelIssueTaskParams{
				AgentID: util.MustParseUUID(f.agentID), RuntimeID: util.MustParseUUID(runtimeID), IssueID: util.MustParseUUID(issueID),
			})
			assertNoTask("CreateDeferredChannelIssueTask", deferred, err)
			wakeup, err := q.CreateWakeupTask(ctx, db.CreateWakeupTaskParams{
				AgentID: util.MustParseUUID(f.agentID), RuntimeID: util.MustParseUUID(runtimeID), IssueID: util.MustParseUUID(issueID), WakeupContext: []byte(`{}`),
			})
			assertNoTask("CreateWakeupTask", wakeup, err)
			retry, err := q.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: util.MustParseUUID(parentID)})
			assertNoTask("CreateRetryTask", retry, err)
		})
	}
}

func TestOrdinaryTerminalIssueTaskCreationIsUnchanged(t *testing.T) {
	f := newRequirementIssueGateFixture(t)
	issueID := f.fixture.Issue(t, "Ordinary done issue", testutil.Cols{
		"status": "done", "assignee_type": "agent", "assignee_id": f.agentID,
	})
	f.fixture.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	var runtimeID string
	f.fixture.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.agentID).Scan(&runtimeID)
	task, err := db.New(sharedTestPool(t)).CreateAgentTask(context.Background(), db.CreateAgentTaskParams{
		AgentID: util.MustParseUUID(f.agentID), RuntimeID: util.MustParseUUID(runtimeID), IssueID: util.MustParseUUID(issueID),
	})
	if err != nil || !task.ID.Valid {
		t.Fatalf("ordinary done issue task = %+v, %v", task, err)
	}
}
