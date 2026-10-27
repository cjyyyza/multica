package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestRequirementTaskLifecycleOwnsStartAndReview(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "todo"})
	ctx := context.Background()
	if _, err := sharedTestPool(t).Exec(ctx, `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	svc := &TaskService{Queries: f.queries, TxStarter: sharedTestPool(t), Bus: events.New()}
	if _, err := svc.StartTask(ctx, requirementTestUUID(t, f.taskID)); err != nil {
		t.Fatalf("start requirement task: %v", err)
	}
	if got := f.currentStatus(t); got != "in_progress" {
		t.Fatalf("status after start = %s, want in_progress", got)
	}
	if _, transitioned, err := svc.CompleteTaskWithTransition(ctx, requirementTestUUID(t, f.taskID), nil, "", "", "", false, "", ""); err != nil || !transitioned {
		t.Fatalf("complete requirement task: transitioned=%v err=%v", transitioned, err)
	}
	if got := f.currentStatus(t); got != "in_review" {
		t.Fatalf("status after complete = %s, want in_review", got)
	}
}

func TestRequirementTaskCompleteAfterCancelIsIdempotent(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "cancelled"})
	if _, err := sharedTestPool(t).Exec(context.Background(), `UPDATE requirement SET cancelled_at=now() WHERE id=$1`, f.requirementID); err != nil {
		t.Fatal(err)
	}
	svc := &TaskService{Queries: f.queries, TxStarter: sharedTestPool(t), Bus: events.New()}
	if _, transitioned, err := svc.CompleteTaskWithTransition(context.Background(), requirementTestUUID(t, f.taskID), nil, "", "", "", false, "", ""); err != nil || !transitioned {
		t.Fatalf("complete cancelled task: transitioned=%v err=%v", transitioned, err)
	}
	if got := f.currentStatus(t); got != "cancelled" {
		t.Fatalf("completion resurrected cancelled issue to %s", got)
	}
}

func TestRequirementTaskCompleteDoesNotSkipBlockedQuestion(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "blocked"})
	svc := &TaskService{Queries: f.queries, TxStarter: sharedTestPool(t), Bus: events.New()}
	if _, transitioned, err := svc.CompleteTaskWithTransition(context.Background(), requirementTestUUID(t, f.taskID), nil, "", "", "", false, "", ""); err != nil || !transitioned {
		t.Fatalf("complete blocked task: transitioned=%v err=%v", transitioned, err)
	}
	if got := f.currentStatus(t); got != "blocked" {
		t.Fatalf("status after blocked completion = %s, want blocked", got)
	}
}

func TestRequirementWakeupResumeRequiresOwnedAnswer(t *testing.T) {
	f := newRequirementStatusGateFixture(t, requirementStatusGateFixtureOpts{status: "blocked"})
	ctx := context.Background()
	task, err := f.queries.GetAgentTask(ctx, requirementTestUUID(t, f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceRequirementTaskStart(ctx, f.queries, task); err == nil {
		t.Fatal("ordinary task resumed a blocked requirement without an answer")
	}
	if got := f.currentStatus(t); got != "blocked" {
		t.Fatalf("unowned resume changed status to %s", got)
	}

	comment, err := f.queries.CreateComment(ctx, db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: task.IssueID, WorkspaceID: requirementTestUUID(t, f.workspaceID),
		AuthorType: "member", AuthorID: requirementTestUUID(t, f.userID), Content: "answer", Type: "comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	wakeupID := dbid.NewV7()
	if _, err := sharedTestPool(t).Exec(ctx, `
		INSERT INTO issue_wakeup(
			id,workspace_id,issue_id,agent_id,created_by,instruction,kind,mode,
			requirement_id,parent_comment_id,enabled,last_triggered_comment_id,last_task_id
		) VALUES($1,$2,$3,$4,$5,'answer me','event','once',$6,$7,false,$7,$8)`,
		wakeupID, requirementTestUUID(t, f.workspaceID), task.IssueID, task.AgentID, requirementTestUUID(t, f.userID),
		requirementTestUUID(t, f.requirementID), comment.ID, task.ID,
	); err != nil {
		t.Fatal(err)
	}
	contextJSON, err := json.Marshal(map[string]any{
		"wakeup_id": wakeupID.String(), "requirement_question_comment_id": comment.ID.String(),
		"requirement_wakeup_generation": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedTestPool(t).Exec(ctx, `UPDATE agent_task_queue SET context=$2,trigger_comment_id=$3 WHERE id=$1`, task.ID, contextJSON, comment.ID); err != nil {
		t.Fatal(err)
	}
	task, err = f.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceRequirementTaskStart(ctx, f.queries, task); err != nil {
		t.Fatalf("resume with owned answer: %v", err)
	}
	if got := f.currentStatus(t); got != "in_progress" {
		t.Fatalf("status after owned answer = %s, want in_progress", got)
	}
}
