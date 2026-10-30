package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRequirementPendingMergeRechecksLockedRow(t *testing.T) {
	for _, transition := range []string{"bind another thread", "dispatch"} {
		t.Run(transition, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			runtimeID := dbfx.Runtime(t, "Requirement merge race")
			agentID := dbfx.Agent(t, "Requirement merge race", runtimeID)
			issueID := dbfx.Issue(t, "Requirement merge race")
			first := dbfx.Comment(t, issueID, "First thread instruction")
			second := dbfx.Comment(t, issueID, "Independent thread instruction")
			taskID := dbfx.Task(t, agentID, testutil.Cols{
				"runtime_id": runtimeID, "issue_id": issueID, "status": "deferred",
				"context": []byte(`{"requirement_delegation":true}`),
			})
			params := func(commentID string) db.MergeCommentIntoPendingTaskParams {
				return db.MergeCommentIntoPendingTaskParams{
					IssueID: parseUUID(issueID), AgentID: parseUUID(agentID), NewTriggerCommentID: parseUUID(commentID),
					NewOriginatorUserID: parseUUID(testUserID), NewAccountableUserID: parseUUID(testUserID),
					NewOriginatorSource: pgtype.Text{String: "direct_human", Valid: true},
				}
			}
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if transition == "bind another thread" {
				_, err = db.New(tx).MergeCommentIntoPendingTask(ctx, params(first))
			} else {
				_, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, taskID)
			}
			if err != nil {
				t.Fatal(err)
			}
			conn, err := testPool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			done := make(chan struct{})
			var mergeErr error
			go func() {
				_, mergeErr = db.New(conn).MergeCommentIntoPendingTask(ctx, params(second))
				close(done)
			}()
			defer func() { cancel(); <-done }()
			for {
				var blocked bool
				if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, conn.Conn().PgConn().PID()).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("merge did not wait for the changing task row")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			if !errors.Is(mergeErr, pgx.ErrNoRows) {
				t.Fatalf("merge after %s = %v, want no rows", transition, mergeErr)
			}
			task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
			if err != nil {
				t.Fatal(err)
			}
			if task.TriggerCommentID == parseUUID(second) || len(task.CoalescedCommentIds) != 0 {
				t.Fatalf("unrelated input reached the run: trigger=%v coalesced=%v", task.TriggerCommentID, task.CoalescedCommentIds)
			}
			if transition == "bind another thread" && task.CommentThreadID != parseUUID(first) {
				t.Fatal("concurrent merge changed the first thread binding")
			}
		})
	}
}

func TestRequirementReworkOverlayUsesExpectedTrigger(t *testing.T) {
	ctx := t.Context()
	runtimeID := dbfx.Runtime(t, "Requirement rework overlay")
	agentID := dbfx.Agent(t, "Requirement rework overlay", runtimeID)
	issueID := dbfx.Issue(t, "Requirement rework overlay")
	rejection := dbfx.Comment(t, issueID, "Rejection input")
	later := dbfx.Comment(t, issueID, "Later input", testutil.Cols{"parent_id": rejection})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "deferred",
		"context": []byte(`{"requirement_delegation":true}`), "trigger_comment_id": rejection,
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
	})
	params := db.SetDeferredChannelIssueTaskRuntimeOverlayParams{
		ID: parseUUID(taskID), RuntimeMcpOverlay: []byte(`{"marker":"rework"}`),
		ExpectedOriginatorUserID: parseUUID(testUserID), ExpectedTriggerCommentID: parseUUID(rejection),
	}
	updated, err := testHandler.Queries.SetDeferredChannelIssueTaskRuntimeOverlay(ctx, params)
	if err != nil || updated != 1 {
		t.Fatalf("original rejection overlay was not hydrated: updated=%d error=%v", updated, err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET trigger_comment_id=$2 WHERE id=$1`, taskID, later)
	updated, err = testHandler.Queries.SetDeferredChannelIssueTaskRuntimeOverlay(ctx, params)
	if err != nil || updated != 0 {
		t.Fatalf("stale rejection overlay replaced a new input: updated=%d error=%v", updated, err)
	}
}
