package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func registerAllRequirementBottomCleanup(t *testing.T, issueID pgtype.UUID) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id = $1)`, issueID)
	dbfx.Cleanup(t, `DELETE FROM activity_log WHERE issue_id = $1 AND action IN ('wakeup_created', 'wakeup_triggered')`, issueID)
}

func blockRequirementForTest(t *testing.T, f requirementReviewFixture, taskID, question, key string) requirementBlockResponse {
	t.Helper()
	response := testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, taskID, question, testUserID, f.Issue.Revision, key)).Want(http.StatusOK)
	var blocked requirementBlockResponse
	response.JSON(&blocked)
	registerAllRequirementBottomCleanup(t, f.Issue.ID)
	return blocked
}

func createRequirementAnswerForTest(t *testing.T, issueID pgtype.UUID, content string) string {
	t.Helper()
	var response CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+uuidToString(issueID)+"/comments", map[string]any{"content": content}), "id", uuidToString(issueID))).Want(http.StatusCreated).JSON(&response)
	return response.ID
}

func requirementRecoveryTasksForTest(t *testing.T, f requirementReviewFixture, questionCommentID string) []db.AgentTaskQueue {
	t.Helper()
	rows, err := testHandler.Queries.ListRequirementCommentCoverage(context.Background(), db.ListRequirementCommentCoverageParams{
		IssueID: f.Issue.ID, RequirementID: parseUUID(f.Detail.ID), QuestionCommentID: questionCommentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := make([]db.AgentTaskQueue, 0, len(rows))
	for _, row := range rows {
		task, err := testHandler.Queries.GetAgentTask(context.Background(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func tickRequirementRecoveryForTest(t *testing.T) error {
	t.Helper()
	return (&service.IssueWakeupService{Tasks: testHandler.TaskService}).TickWorkspaces(context.Background(), parseUUID(testWorkspaceID))
}

func completeRequirementRecoveryTaskForTest(t *testing.T, taskID string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, taskID)
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(taskID), []byte(`{"output":""}`), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
}

func requirementWakeupForTest(t *testing.T, f requirementReviewFixture, questionCommentID string) db.IssueWakeup {
	t.Helper()
	rows, err := testHandler.Queries.ListRequirementIssueWakeups(context.Background(), db.ListRequirementIssueWakeupsParams{
		WorkspaceID: f.Issue.WorkspaceID, RequirementID: parseUUID(f.Detail.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	var found *db.IssueWakeup
	for i, row := range rows {
		if row.ParentCommentID != parseUUID(questionCommentID) {
			continue
		}
		if !row.DisabledAt.Valid {
			return row
		}
		if found == nil {
			found = &rows[i]
		}
	}
	if found != nil {
		return *found
	}
	t.Fatalf("requirement wakeup for question %s not found", questionCommentID)
	return db.IssueWakeup{}
}

func TestRequirementWakeupRunDuringCommentsAndRepeatedRearm(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97101")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need deployment target", "recovery-block-97101")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)

	firstAnswer := createRequirementAnswerForTest(t, f.Issue.ID, "Use production")
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory')`, f.Issue.ID); got != 0 {
		t.Fatalf("ordinary comment path created a duplicate recovery run: %d", got)
	}
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	firstRuns := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(firstRuns) != 1 || firstRuns[0].TriggerCommentID != parseUUID(firstAnswer) {
		t.Fatalf("first recovery runs = %+v", firstRuns)
	}
	firstRun := firstRuns[0]
	if uuidToString(firstRun.RequirementID) != f.Detail.ID {
		t.Fatalf("first recovery requirement = %s", uuidToString(firstRun.RequirementID))
	}
	var generation int64
	var wakeupID string
	if err := testPool.QueryRow(context.Background(), `SELECT (context->>'requirement_wakeup_generation')::bigint, context->>'wakeup_id' FROM agent_task_queue WHERE id = $1`, firstRun.ID).Scan(&generation, &wakeupID); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || wakeupID != blocked.Wakeup.ID {
		t.Fatalf("first recovery context generation=%d wakeup=%s", generation, wakeupID)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, firstRun.ID)

	secondAnswer := createRequirementAnswerForTest(t, f.Issue.ID, "Use the staging fallback too")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
		t.Fatalf("comment during active run created another run early: %d", got)
	}
	completeRequirementRecoveryTaskForTest(t, uuidToString(firstRun.ID))
	secondRuns := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(secondRuns) != 2 {
		t.Fatalf("comment during run was not recovered: %d runs", len(secondRuns))
	}
	secondRun := secondRuns[1]
	if secondRun.TriggerCommentID != parseUUID(secondAnswer) {
		t.Fatalf("second recovery trigger = %s, want %s", uuidToString(secondRun.TriggerCommentID), secondAnswer)
	}
	wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if uuidToString(wakeup.ID) != blocked.Wakeup.ID || wakeup.Revision != 2 || wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("wakeup after first rearm = %+v", wakeup)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id = $1 AND processed_at IS NOT NULL`, blocked.Wakeup.ID); got == 0 {
		t.Fatal("first revision receipts were not processed")
	}

	completeRequirementRecoveryTaskForTest(t, uuidToString(secondRun.ID))
	wakeup = requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if wakeup.Revision != 3 || !wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("wakeup after quiet recovery = %+v", wakeup)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 2 {
		t.Fatalf("quiet rearm created an unnecessary run: %d", got)
	}
	thirdAnswer := createRequirementAnswerForTest(t, f.Issue.ID, "The fallback is optional, not required")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	thirdRuns := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(thirdRuns) != 3 || thirdRuns[2].TriggerCommentID != parseUUID(thirdAnswer) {
		t.Fatalf("second generation did not wake: runs=%+v answer=%s", thirdRuns, thirdAnswer)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked'`, f.Detail.ID); got != 1 {
		t.Fatalf("rearm duplicated blocked notifications: %d", got)
	}
}

func TestRequirementWakeupCoalescesCanonicalCommentsOnce(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97102")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need one answer", "recovery-block-97102")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)
	first := createRequirementAnswerForTest(t, f.Issue.ID, "first detail")
	second := createRequirementAnswerForTest(t, f.Issue.ID, "second detail")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 1 || runs[0].TriggerCommentID != parseUUID(second) || len(runs[0].CoalescedCommentIds) != 1 || runs[0].CoalescedCommentIds[0] != parseUUID(first) {
		t.Fatalf("coalesced recovery = %+v", runs)
	}
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
		t.Fatalf("same canonical comments ran twice: %d", got)
	}
	completeRequirementRecoveryTaskForTest(t, uuidToString(runs[0].ID))
	wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if wakeup.Revision != 2 || !wakeup.Enabled || len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)) != 1 {
		t.Fatalf("covered comments were replayed during rearm: %+v", wakeup)
	}
	if wakeup.LastTriggeredCommentID != parseUUID(second) {
		t.Fatalf("last triggered comment = %s, want %s", uuidToString(wakeup.LastTriggeredCommentID), second)
	}
}

func TestRequirementWakeupConcurrentRecoveryUsesGenerationCAS(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97103")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need one answer", "recovery-block-97103")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)
	createRequirementAnswerForTest(t, f.Issue.ID, "answer")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 1 {
		t.Fatalf("initial recovery runs = %d", len(runs))
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, runs[0].ID)
	if wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID); wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("completed run was not left triggered: %+v", wakeup)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- (&service.IssueWakeupService{Tasks: testHandler.TaskService}).TickWorkspaces(context.Background(), parseUUID(testWorkspaceID))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if uuidToString(wakeup.ID) != blocked.Wakeup.ID || wakeup.Revision != 2 || !wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("concurrent rearm = %+v", wakeup)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`, f.Issue.ID); got != 1 {
		t.Fatalf("concurrent rearm created duplicate wakeups: %d", got)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
		t.Fatalf("concurrent rearm duplicated recovery runs: %d", got)
	}
}

func TestRequirementWakeupRearmCommitRollback(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97105")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need one answer", "recovery-block-97105")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)
	createRequirementAnswerForTest(t, f.Issue.ID, "answer")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 1 {
		t.Fatalf("initial recovery runs = %d", len(runs))
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, runs[0].ID)
	previous := testHandler.TaskService.TxStarter
	testHandler.TaskService.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: fmt.Errorf("rearm commit failure")}
	t.Cleanup(func() { testHandler.TaskService.TxStarter = previous })
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), runs[0].ID, []byte(`{"output":""}`), "", "", "", false, "", ""); err == nil {
		testHandler.TaskService.TxStarter = previous
		t.Fatal("expected rearm commit failure")
	}
	testHandler.TaskService.TxStarter = previous
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, runs[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("failed rearm changed task status to %s", status)
	}
	wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if wakeup.Revision != 1 || wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("failed rearm changed wakeup state: %+v", wakeup)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`, f.Issue.ID); got != 1 {
		t.Fatalf("failed rearm left another wakeup: %d", got)
	}
	completeRequirementRecoveryTaskForTest(t, uuidToString(runs[0].ID))
	wakeup = requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if wakeup.Revision != 2 || !wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("successful retry did not rearm: %+v", wakeup)
	}
}

// A question lives in exactly one issue_wakeup row. Deleting that row removes
// the wakeup instead of leaving a logical record that silently rebuilds it.
func TestRequirementWakeupMissingRowIsNotResurrected(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97106")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need one answer", "recovery-block-97106")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)
	dbfx.Exec(t, `DELETE FROM issue_wakeup WHERE id = $1`, blocked.Wakeup.ID)
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`, f.Issue.ID); got != 0 {
		t.Fatalf("missing wakeup row was resurrected: %d", got)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 0 {
		t.Fatalf("missing wakeup row still produced runs: %d", got)
	}
}

func TestRequirementWakeupLeaveReenterCancelAndDeleteFences(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97104")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need one answer", "recovery-block-97104")
	// Answering the blocking question resumes execution through the explicit
	// task action; a generic task-token status write stays forbidden.
	if _, err := service.UpdateIssueStatusWithRequirementGate(context.Background(), testHandler.Queries, db.UpdateIssueStatusParams{
		ID: f.Issue.ID, WorkspaceID: f.Issue.WorkspaceID, Status: "in_progress",
		SourceTaskID:      parseUUID(blockingTaskID),
		RequirementAction: service.RequirementIssueAction(service.RequirementIssueActionWakeupResume),
	}); err != nil {
		t.Fatalf("resume blocked issue: %v", err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE id = $1 AND enabled = false AND disabled_at IS NOT NULL`, blocked.Wakeup.ID); got != 1 {
		t.Fatal("leaving blocked did not fence the question wakeup")
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Issue = issue
	again := blockRequirementForTest(t, f, blockingTaskID, "Need a new answer", "recovery-reblock-97104")
	if again.Wakeup.ID == blocked.Wakeup.ID {
		t.Fatal("re-entering blocked reused the old question wakeup")
	}
	issue, err = testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
		service.CancelRequirementRequest{ExpectedIssueRevision: ptr(issue.Revision)}, "recovery-cancel-97104")).Want(http.StatusOK)
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE id = $1 AND disabled_at IS NOT NULL`, again.Wakeup.ID); got != 1 {
		t.Fatal("cancel did not fence the new question wakeup")
	}
	testutil.Call(t, testHandler.DeleteIssue, withURLParam(newRequest(http.MethodDelete, "/api/issues/"+uuidToString(f.Issue.ID), nil), "id", uuidToString(f.Issue.ID))).Want(http.StatusNoContent)
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, again.QuestionComment.ID)); got != 0 {
		t.Fatalf("cancel/delete left recovery runs: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`, f.Issue.ID); got != 0 {
		t.Fatalf("deleted issue left wakeups: %d", got)
	}
}
