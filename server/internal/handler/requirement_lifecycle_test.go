package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type requirementReviewFixture struct {
	requirementApproveFixture
	Issue         db.Issue
	InitialTaskID string
}

func newRequirementReviewFixture(t *testing.T, externalID string) requirementReviewFixture {
	t.Helper()
	f := newRequirementApproveFixture(t, externalID)
	approved, _ := approveRequirementForTest(t, f, "review-approve-"+externalID)
	f.Detail = approved.Requirement
	issue := approveIssueForTest(t, approved)
	var initialTaskID string
	dbfx.QueryRow(t, `SELECT initial_task_id::text FROM requirement_approval WHERE requirement_id = $1`, f.Detail.ID).Scan(&initialTaskID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, initialTaskID)
	dbfx.Exec(t, `UPDATE issue SET status = 'in_review', revision = revision + 1 WHERE id = $1`, issue.ID)
	row, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	return requirementReviewFixture{requirementApproveFixture: f, Issue: row, InitialTaskID: initialTaskID}
}

func requirementActionRequest(userID, action, requirementID string, input any, key string) *http.Request {
	return requirementRequest(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+requirementID+"/"+action, input, key,
		"id", testWorkspaceID, "requirementId", requirementID)
}

func acceptRequirementForTest(t *testing.T, f requirementReviewFixture, key string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID,
		service.AcceptRequirementRequest{ExpectedIssueRevision: f.Issue.Revision}, key)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func rejectRequirementForTest(t *testing.T, f requirementReviewFixture, reason, key string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", f.Detail.ID,
		service.RejectRequirementRequest{ExpectedIssueRevision: f.Issue.Revision, Reason: reason}, key)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func createRequirementReviewWakeup(t *testing.T, f requirementReviewFixture) (db.IssueWakeup, string) {
	t.Helper()
	svc := service.IssueWakeupService{Tasks: testHandler.TaskService}
	wakeup, err := svc.Create(context.Background(), f.Issue.ID, parseUUID(testUserID), pgtype.UUID{}, service.WakeupInput{
		AgentID: f.AgentID, Kind: "event", EventTypes: []string{"task.completed"}, Instruction: "follow up the review",
	})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup WHERE id = $1`, wakeup.ID)
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup_receipt WHERE wakeup_id = $1`, wakeup.ID)
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.AgentID).Scan(&runtimeID)
	taskID := dbfx.Task(t, f.AgentID, testutil.Cols{
		"issue_id": f.Issue.ID, "runtime_id": runtimeID, "status": "queued",
		"context": fmt.Sprintf(`{"wakeup_id":%q}`, uuidToString(wakeup.ID)),
	})
	return wakeup, taskID
}

func TestAcceptRequirementWritebackWakeupRunAndReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96001")
	wakeup, wakeupTaskID := createRequirementReviewWakeup(t, f)
	capture := newRequirementRealtimeCapture(t)
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	detail := acceptRequirementForTest(t, f, "accept-success-96001")
	if detail.State != "done" || detail.Revision != f.Detail.Revision+1 || detail.IssueID == nil || *detail.IssueID != uuidToString(f.Issue.ID) {
		t.Fatalf("accepted detail = %+v", detail)
	}
	var issue db.Issue
	var err error
	issue, err = testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "done" || issue.Revision != f.Issue.Revision+1 {
		t.Fatalf("accepted issue = status %s revision %d", issue.Status, issue.Revision)
	}
	var operationID, operationState string
	var rawPayload []byte
	dbfx.QueryRow(t, `SELECT id::text, state, payload FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID).Scan(&operationID, &operationState, &rawPayload)
	if operationState != "pending" {
		t.Fatalf("writeback state = %s, want pending", operationState)
	}
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Revision != issue.Revision || payload.ExpectedDigest != f.Source.Digest || payload.Source != f.Source.Source || payload.Channel == nil || payload.Channel.InstallationID != f.InstallationID {
		t.Fatalf("writeback payload = %+v", payload)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND operation_id = $2 AND issue_revision = $3 AND status = 'pending'`, f.Detail.ID, operationID, issue.Revision); got != 1 {
		t.Fatalf("writeback outbox = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE id = $1 AND enabled = false AND disabled_at IS NOT NULL`, wakeup.ID); got != 1 {
		t.Fatal("accept did not disable the wakeup")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'cancelled'`, wakeupTaskID); got != 1 {
		t.Fatal("accept did not cancel the unstarted wakeup run")
	}
	var run AgentTaskResponse
	if err := json.Unmarshal(detail.Run, &run); err != nil || run.ID != f.InitialTaskID || run.RequirementID != f.Detail.ID {
		t.Fatalf("accepted run = %+v, err=%v", run, err)
	}
	foundPublish := false
	for _, operation := range detail.Operations {
		if operation.ID == operationID && operation.Kind == "publish" && operation.State == "pending" {
			foundPublish = true
		}
	}
	if !foundPublish {
		t.Fatalf("accepted operations = %+v", detail.Operations)
	}
	assertRequirementRealtimeEvent(t, capture.last(), f.Detail.ID, detail.Revision)
	original := requirementJSON(detail)
	replayed := acceptRequirementForTest(t, f, "accept-success-96001")
	if !requirementSameJSON(t, string(original), string(requirementJSON(replayed))) {
		t.Fatalf("accept replay changed response: %+v", replayed)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID); got != 1 {
		t.Fatalf("publish operations after replay = %d", got)
	}
	conflict := testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID,
		service.AcceptRequirementRequest{ExpectedIssueRevision: issue.Revision}, "accept-after-done-96001")).Want(http.StatusConflict)
	if conflict.Map()["code"] != service.RequirementNotInReviewCode {
		t.Fatalf("accept after done = %s", conflict.Body)
	}
}

func TestRejectRequirementCommentRevisionTaskAndReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96002")
	capture := newRequirementRealtimeCapture(t)
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	detail := rejectRequirementForTest(t, f, "  needs another pass  ", "reject-success-96002")
	if detail.State != "executing" || detail.Revision != f.Detail.Revision+1 {
		t.Fatalf("rejected detail = %+v", detail)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "in_progress" || issue.Revision != f.Issue.Revision+2 {
		t.Fatalf("rejected issue = status %s revision %d, want in_progress and comment+status revisions", issue.Status, issue.Revision)
	}
	var commentID, authorType, content string
	dbfx.QueryRow(t, `SELECT id::text, author_type, content FROM comment WHERE issue_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, f.Issue.ID).Scan(&commentID, &authorType, &content)
	if authorType != "member" || content != "needs another pass" {
		t.Fatalf("rejection comment = author %s content %q", authorType, content)
	}
	var taskID, taskRequirementID, taskStatus string
	dbfx.QueryRow(t, `SELECT id::text, requirement_id::text, status FROM agent_task_queue WHERE issue_id = $1 AND id <> $2 ORDER BY created_at DESC, id DESC LIMIT 1`, f.Issue.ID, f.InitialTaskID).Scan(&taskID, &taskRequirementID, &taskStatus)
	if taskID == f.InitialTaskID || taskRequirementID != f.Detail.ID || taskStatus != "queued" {
		t.Fatalf("rejection task = id %s requirement %s status %s", taskID, taskRequirementID, taskStatus)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND issue_revision = $2 AND status = 'pending'`, f.Detail.ID, issue.Revision); got != 0 {
		t.Fatalf("rejection sent a routine notification: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_candidate WHERE requirement_id = $1`, f.Detail.ID); got != 1 {
		t.Fatalf("reject created a candidate: %d", got)
	}
	var run AgentTaskResponse
	if err := json.Unmarshal(detail.Run, &run); err != nil || run.ID != taskID || run.Mode != "execute" || run.RequirementID != f.Detail.ID {
		t.Fatalf("rejection run = %+v, err=%v", run, err)
	}
	foundCommentEvent := false
	for _, event := range capture.events {
		if event.Type != protocol.EventCommentCreated {
			continue
		}
		payload := event.Payload.(map[string]any)
		if payload["issue_revision"] != issue.Revision {
			t.Fatalf("comment event revision = %v, want %d", payload["issue_revision"], issue.Revision)
		}
		foundCommentEvent = true
	}
	if !foundCommentEvent {
		t.Fatal("missing rejection comment event")
	}
	assertRequirementRealtimeEvent(t, capture.last(), f.Detail.ID, detail.Revision)
	original := requirementJSON(detail)
	replayed := rejectRequirementForTest(t, f, "  needs another pass  ", "reject-success-96002")
	if !requirementSameJSON(t, string(original), string(requirementJSON(replayed))) {
		t.Fatalf("reject replay changed response: %+v", replayed)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.Issue.ID); got != 1 {
		t.Fatalf("comments after replay = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND mode = 'execute'`, f.Issue.ID); got != 2 {
		t.Fatalf("execute tasks after replay = %d", got)
	}
	second := testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", f.Detail.ID,
		service.RejectRequirementRequest{ExpectedIssueRevision: issue.Revision, Reason: "second rejection"}, "reject-again-96002")).Want(http.StatusConflict)
	if second.Map()["code"] != service.RequirementNotInReviewCode {
		t.Fatalf("second rejection response = %s", second.Body)
	}
}

func TestCancelRequirementWithoutIssueFencesIntakeAndOutbox(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement cancel intake agent")
	source := newRequirementSourceFixture("96003")
	operationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "preview", "state": "succeeded",
		"payload": requirementJSON(yixiezuo.OperationPayload{Source: source.Source}), "result": source.Raw,
	})
	body := source.body(agentID)
	body["intakeOperationId"] = operationID
	detail := createRequirementForTest(t, testUserID, body, "cancel-intake-create-96003")
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET state = 'pending', completed_at = NULL WHERE id = $1`, operationID)
	input := service.CancelRequirementRequest{ExpectedRevision: ptr(int64(1)), Reason: "  withdraw intake  "}
	var cancelled service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", detail.ID, input, "cancel-intake-96003")).Want(http.StatusOK).JSON(&cancelled)
	if cancelled.State != "cancelled" || cancelled.CancelledAt == nil || cancelled.CancelReason == nil || *cancelled.CancelReason != "withdraw intake" || cancelled.IssueID != nil {
		t.Fatalf("cancel intake detail = %+v", cancelled)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_draft' AND status = 'superseded'`, detail.ID); got != 1 {
		t.Fatalf("intake outbox was not superseded: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE id = $1 AND state = 'failed' AND error LIKE 'Requirement was cancelled before%'`, operationID); got != 1 {
		t.Fatalf("pending intake was not fenced: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status' AND status = 'pending'`, detail.ID); got != 0 {
		t.Fatalf("cancel sent a routine notification: %d", got)
	}
	if string(cancelled.Issue) != "null" || string(cancelled.Run) != "null" {
		t.Fatalf("cancel intake projections: issue=%s run=%s", cancelled.Issue, cancelled.Run)
	}
}

func TestAcceptedRequirementCannotCancelAndAcceptReplayStillWorks(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96032")
	accepted := acceptRequirementForTest(t, f, "terminal-accept-96032")
	replayedAccept := acceptRequirementForTest(t, f, "terminal-accept-96032")
	if !requirementSameJSON(t, string(requirementJSON(accepted)), string(requirementJSON(replayedAccept))) {
		t.Fatal("same-key accept replay changed the accepted requirement")
	}
	acceptedIssue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
		service.CancelRequirementRequest{ExpectedRevision: ptr(accepted.Revision), ExpectedIssueRevision: ptr(acceptedIssue.Revision)}, "terminal-cancel-96032")).Want(http.StatusConflict)
	if response.Map()["code"] != "requirement_done" {
		t.Fatalf("done cancel response = %s", response.Body)
	}
	if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "done" {
		t.Fatalf("done cancel changed issue to %s", got)
	}
	detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.Detail.ID)
	if detail.State != "done" || detail.CancelledAt != nil {
		t.Fatalf("done cancel changed requirement = %+v", detail)
	}
}

func TestCancelRequirementWithIssueCancelsTasksAndFencesWriteback(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96004")
	dbfx.Exec(t, `UPDATE issue SET status = 'in_progress', revision = revision + 1 WHERE id = $1`, f.Issue.ID)
	executingIssue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "state": "pending",
		"issue_id": f.Issue.ID, "requirement_id": f.Detail.ID, "payload": []byte(`{}`),
	})
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.AgentID).Scan(&runtimeID)
	runningID := dbfx.Task(t, f.AgentID, testutil.Cols{"issue_id": f.Issue.ID, "runtime_id": runtimeID, "requirement_id": f.Detail.ID, "mode": "execute", "status": "running", "started_at": testutil.Raw("now()")})
	queuedID := dbfx.Task(t, f.AgentID, testutil.Cols{"issue_id": f.Issue.ID, "runtime_id": runtimeID, "requirement_id": f.Detail.ID, "mode": "execute", "status": "queued"})
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	input := service.CancelRequirementRequest{
		ExpectedRevision: ptr(f.Detail.Revision), ExpectedIssueRevision: ptr(executingIssue.Revision), Reason: "  stop execution  ",
	}
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID, input, "cancel-issue-96004")).Want(http.StatusOK).JSON(&detail)
	if detail.State != "cancelled" || detail.CancelReason == nil || *detail.CancelReason != "stop execution" {
		t.Fatalf("cancel issue detail = %+v", detail)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "cancelled" || issue.Revision != executingIssue.Revision+1 {
		t.Fatalf("cancelled issue = status %s revision %d", issue.Status, issue.Revision)
	}
	for _, taskID := range []string{runningID, queuedID} {
		if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'cancelled'`, taskID); got != 1 {
			t.Fatalf("active task was not cancelled: %s", taskID)
		}
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish' AND state = 'failed' AND error LIKE 'Requirement was cancelled before writeback started'`, f.Issue.ID); got != 1 {
		t.Fatalf("pending writeback was not fenced: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status' AND status = 'superseded'`, f.Detail.ID); got == 0 {
		t.Fatal("previous outbox was not superseded")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status' AND status = 'pending' AND issue_revision = $2`, f.Detail.ID, issue.Revision); got != 0 {
		t.Fatalf("cancel sent a routine notification: %d", got)
	}
	var run AgentTaskResponse
	if err := json.Unmarshal(detail.Run, &run); err != nil || run.ID == "" || run.Status != "cancelled" {
		t.Fatalf("cancel run = %+v, err=%v", run, err)
	}
	var replayed service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID, input, "cancel-issue-96004")).Want(http.StatusOK).JSON(&replayed)
	if !requirementSameJSON(t, string(requirementJSON(detail)), string(requirementJSON(replayed))) {
		t.Fatalf("cancel replay changed response: %+v", replayed)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID); got != 1 {
		t.Fatalf("cancel replay duplicated writeback: %d", got)
	}
}

func TestCancelRequirementSettlesCancelledWakeupLedger(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "96031")
	_, wakeupTaskID := createRequirementReviewWakeup(t, f)
	failedTaskID := dbfx.Task(t, f.AgentID, testutil.Cols{
		"issue_id": f.Issue.ID, "status": "failed", "completed_at": testutil.Raw("now()"),
	})
	recoveryCommentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id": f.Issue.ID, "workspace_id": testWorkspaceID, "author_type": "system", "author_id": testUserID,
		"content": "resume delegated coordination", "type": "progress_update", "source_task_id": failedTaskID,
	})
	dbfx.Exec(t, `UPDATE agent_task_queue SET delivered_comment_ids = ARRAY[$2]::uuid[] WHERE id = $1`, wakeupTaskID, recoveryCommentID)
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
		service.CancelRequirementRequest{ExpectedIssueRevision: ptr(f.Issue.Revision)}, "cancel-wakeup-ledger-96031")).Want(http.StatusOK)
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'cancelled'`, wakeupTaskID); got != 1 {
		t.Fatal("wakeup task was not cancelled")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1 AND recovery_settled_at IS NOT NULL`, recoveryCommentID); got != 1 {
		t.Fatal("cancelled wakeup task committed without settling its delivered recovery")
	}
}

func TestRequirementLifecycleValidationAndCommitRollback(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("permissions and stale revisions", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96005")
		viewer := dbfx.User(t, "Requirement lifecycle viewer", uuid.NewString()+"@example.test")
		dbfx.Member(t, testWorkspaceID, viewer, "member")
		dbfx.Insert(t, "requirement_participant", testutil.Cols{"workspace_id": testWorkspaceID, "requirement_id": f.Detail.ID, "user_id": viewer, "role": "viewer"})
		for _, action := range []string{"accept", "reject", "cancel"} {
			var input any = service.AcceptRequirementRequest{ExpectedIssueRevision: f.Issue.Revision}
			if action == "reject" {
				input = service.RejectRequirementRequest{ExpectedIssueRevision: f.Issue.Revision, Reason: "no"}
			} else if action == "cancel" {
				input = service.CancelRequirementRequest{ExpectedIssueRevision: ptr(f.Issue.Revision)}
			}
			testutil.Call(t, map[string]http.HandlerFunc{"accept": testHandler.AcceptRequirement, "reject": testHandler.RejectRequirement, "cancel": testHandler.CancelRequirement}[action],
				requirementActionRequest(viewer, action, f.Detail.ID, input, "lifecycle-viewer-"+action+"-96005")).Want(http.StatusForbidden)
		}
		stale := testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", f.Detail.ID,
			service.RejectRequirementRequest{ExpectedIssueRevision: f.Issue.Revision - 1, Reason: "stale"}, "reject-stale-96005")).Want(http.StatusConflict)
		if stale.Map()["code"] != "revision_conflict" || stale.Map()["actual_revision"] != float64(f.Issue.Revision) {
			t.Fatalf("stale rejection = %s", stale.Body)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.Issue.ID); got != 0 {
			t.Fatalf("stale rejection wrote comments = %d", got)
		}
	})
	t.Run("blank reject reason", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96006")
		response := testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", f.Detail.ID,
			service.RejectRequirementRequest{ExpectedIssueRevision: f.Issue.Revision, Reason: " \t "}, "reject-blank-96006")).Want(http.StatusBadRequest)
		if response.Map()["code"] != "requirement_reject_reason_required" {
			t.Fatalf("blank rejection = %s", response.Body)
		}
	})
	t.Run("missing writeback source rolls back", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96029")
		dbfx.Exec(t, `DELETE FROM yixiezuo_import WHERE issue_id = $1`, f.Issue.ID)
		response := testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID,
			service.AcceptRequirementRequest{ExpectedIssueRevision: f.Issue.Revision}, "accept-missing-source-96029")).Want(http.StatusConflict)
		if response.Map()["code"] != service.RequirementSourceLinkUnavailableCode {
			t.Fatalf("missing source response = %s", response.Body)
		}
		if got := issueStatusForTest(t, uuidToString(f.Issue.ID)); got != "in_review" {
			t.Fatalf("missing source changed issue to %s", got)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID); got != 0 {
			t.Fatalf("missing source created writeback = %d", got)
		}
	})
	t.Run("inflight writeback is not undone", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96030")
		dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
			"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "state": "running",
			"started_at": testutil.Raw("now()"), "issue_id": f.Issue.ID, "requirement_id": f.Detail.ID, "payload": []byte(`{}`),
		})
		dbfx.Exec(t, `UPDATE issue SET status = 'in_progress', revision = revision + 1 WHERE id = $1`, f.Issue.ID)
		executingIssue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := service.CancelRequirementRequest{ExpectedRevision: ptr(f.Detail.Revision), ExpectedIssueRevision: ptr(executingIssue.Revision)}
		testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID, input, "cancel-inflight-96030")).Want(http.StatusOK)
		if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish' AND state = 'running'`, f.Issue.ID); got != 1 {
			t.Fatal("inflight writeback was incorrectly undone")
		}
	})
	t.Run("accept commit rollback", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96007")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: fmt.Errorf("accept commit failure")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID,
			service.AcceptRequirementRequest{ExpectedIssueRevision: f.Issue.Revision}, "accept-rollback-96007")).Want(http.StatusInternalServerError)
		testHandler.TxStarter = previous
		issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if issue.Status != "in_review" || dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID) != 0 {
			t.Fatalf("accept rollback left effects: %+v", issue)
		}
	})
	t.Run("reject commit rollback", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96008")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: fmt.Errorf("reject commit failure")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", f.Detail.ID,
			service.RejectRequirementRequest{ExpectedIssueRevision: f.Issue.Revision, Reason: "rollback"}, "reject-rollback-96008")).Want(http.StatusInternalServerError)
		testHandler.TxStarter = previous
		if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.Issue.ID); got != 0 {
			t.Fatalf("reject rollback comments = %d", got)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND id <> $2`, f.Issue.ID, f.InitialTaskID); got != 0 {
			t.Fatalf("reject rollback tasks = %d", got)
		}
	})
	t.Run("cancel commit rollback", func(t *testing.T) {
		f := newRequirementReviewFixture(t, "96009")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: fmt.Errorf("cancel commit failure")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.Detail.ID,
			service.CancelRequirementRequest{ExpectedIssueRevision: ptr(f.Issue.Revision)}, "cancel-rollback-96009")).Want(http.StatusInternalServerError)
		testHandler.TxStarter = previous
		issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if issue.Status != "in_review" || dbfx.Count(t, `SELECT count(*) FROM requirement WHERE id = $1 AND cancelled_at IS NULL`, f.Detail.ID) != 1 {
			t.Fatalf("cancel rollback left effects: %+v", issue)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status = 'cancelled'`, f.Issue.ID); got != 0 {
			t.Fatalf("cancel rollback cancelled tasks = %d", got)
		}
	})
}
