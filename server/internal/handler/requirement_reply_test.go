package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestNativeRequirementExecutionReplyRecoversAgentAndSquadToAcceptance(t *testing.T) {
	enableRequirementFlowForTest(t)
	for _, target := range []string{"agent", "squad"} {
		t.Run(target, func(t *testing.T) {
			_, agentID := newRequirementClaudeAgent(t, "Reply recovery "+target)
			input := service.CreateNativeRequirementRequest{ExternalRef: "native-execution-reply-" + target, Idea: "Fix login after clarifying the environment", AgentID: agentID}
			if target == "squad" {
				input.AgentID, input.SquadID = "", dbfx.Squad(t, "Reply recovery squad", agentID)
			}
			detail := createNativeRequirementForTest(t, input, "reply-create-"+target)
			if detail.IssueID != nil {
				t.Fatal("Issue exists before human approval")
			}
			plan := startRequirementDelegationTask(t, agentID)
			candidate := submitCandidateForTest(t, agentID, uuidToString(plan.ID), detail.ID, *detail.ChatSessionID, validCandidateRequest(t, detail, detail.Revision), "reply-candidate-"+target)
			completeRequirementDelegationTask(t, plan)
			approved := approveRequirementDelegationForTest(t, candidate, "reply-approve-"+target)
			issue := approveIssueForTest(t, approved)
			execution := startRequirementDelegationTask(t, agentID)
			issueRow, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
			if err != nil {
				t.Fatal(err)
			}
			f := requirementReviewFixture{requirementApproveFixture: requirementApproveFixture{Detail: approved.Requirement, AgentID: agentID}, Issue: issueRow}
			blocked := blockRequirementForTest(t, f, uuidToString(execution.ID), "Which environment?", "reply-block-"+target)
			if blocked.Requirement.BlockedQuestion == nil || blocked.Requirement.BlockedQuestion.CommentID != blocked.QuestionComment.ID || blocked.Requirement.BlockedQuestion.TargetUserID != testUserID || blocked.Requirement.BlockedQuestion.CanReply {
				t.Fatalf("task question projection=%+v", blocked.Requirement.BlockedQuestion)
			}
			var memberDetail service.RequirementDetail
			testutil.Call(t, testHandler.GetRequirementByID, requirementRequest(testUserID, http.MethodGet, "/", nil, "", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusOK).JSON(&memberDetail)
			if memberDetail.BlockedQuestion == nil || !memberDetail.BlockedQuestion.CanReply {
				t.Fatalf("member cannot discover question: %+v", memberDetail.BlockedQuestion)
			}
			reply := service.ReplyRequirementRequest{ExpectedIssueRevision: blocked.QuestionComment.IssueRevision, QuestionCommentID: blocked.QuestionComment.ID, Content: "Use staging"}
			request := func() *http.Request {
				return requirementActionRequest(testUserID, "replies", detail.ID, reply, "reply-answer-"+target)
			}
			var responses [2]*httptest.ResponseRecorder
			var wg sync.WaitGroup
			for i := range responses {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					responses[i] = httptest.NewRecorder()
					testHandler.ReplyRequirement(responses[i], request())
				}(i)
			}
			wg.Wait()
			for _, response := range responses {
				if response.Code != http.StatusCreated {
					t.Fatalf("reply status=%d body=%s", response.Code, response.Body)
				}
			}
			if !requirementSameJSON(t, responses[0].Body.String(), responses[1].Body.String()) {
				t.Fatal("concurrent retry returned a different reply")
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='member'`, issue.ID); got != 1 {
				t.Fatalf("replies=%d", got)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1 AND event_type='comment.created'`, blocked.Wakeup.ID); got != 1 {
				t.Fatalf("durable receipts=%d", got)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id=$1 AND action='reply'`, detail.ID); got != 1 {
				t.Fatalf("reply ledger=%d", got)
			}
			// The original execution still owns the Issue; an answer must not start a parallel run.
			if err := tickRequirementRecoveryForTest(t); err != nil {
				t.Fatal(err)
			}
			if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 0 {
				t.Fatalf("early recovery runs=%d", got)
			}
			completeRequirementDelegationTask(t, execution)
			if err := tickRequirementRecoveryForTest(t); err != nil {
				t.Fatal(err)
			}
			if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
				t.Fatalf("recovery runs=%d", got)
			}
			recovery := startRequirementDelegationTask(t, agentID)
			if recovery.RequirementID != execution.RequirementID || recovery.AgentID != execution.AgentID || recovery.IsLeaderTask != execution.IsLeaderTask {
				t.Fatalf("recovery changed target: %+v", recovery)
			}
			if target == "squad" {
				testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, recovery, "in_review")).Want(http.StatusOK)
			}
			completeRequirementDelegationTask(t, recovery)
			assertRequirementDelegationStatus(t, issue.ID, "in_review")
			testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", detail.ID, service.AcceptRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID)}, "reply-accept-"+target)).Want(http.StatusOK)
			assertRequirementDelegationStatus(t, issue.ID, "done")
			// A retry after acceptance still returns the original receipt, never a new recovery.
			replayed := testutil.Call(t, testHandler.ReplyRequirement, request()).Want(http.StatusCreated)
			if !requirementSameJSON(t, responses[0].Body.String(), replayed.Body.String()) {
				t.Fatal("late replay changed receipt")
			}
			if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
				t.Fatalf("late retry created %d recovery runs", got)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id=$1`, detail.ID); got != 0 {
				t.Fatalf("native reply caused %d source writes", got)
			}
		})
	}
}

func TestRequirementExecutionReplyGuardsAndReplayAuthorization(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementBlockFixture(t, "98301")
	taskID := newRequirementBlockTask(t, f)
	viewer := dbfx.User(t, "Execution answer target", "execution-reply-viewer@multica.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	dbfx.Exec(t, `UPDATE agent SET permission_mode='public_to',visibility='workspace' WHERE id=$1`, f.AgentID)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": f.AgentID, "target_type": "member", "target_id": viewer, "created_by": testUserID})
	dbfx.Exec(t, `INSERT INTO requirement_participant(workspace_id,requirement_id,user_id,role) VALUES ($1,$2,$3,'viewer')`, testWorkspaceID, f.Detail.ID, viewer)
	var blocked requirementBlockResponse
	testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, taskID, "Which target?", viewer, f.Issue.Revision, "reply-guard-block")).Want(http.StatusOK).JSON(&blocked)
	registerAllRequirementBottomCleanup(t, f.Issue.ID)
	input := service.ReplyRequirementRequest{ExpectedIssueRevision: blocked.QuestionComment.IssueRevision, QuestionCommentID: blocked.QuestionComment.ID, Content: "Staging"}
	request := func(user, key string, body service.ReplyRequirementRequest) *http.Request {
		return requirementActionRequest(user, "replies", f.Detail.ID, body, key)
	}
	testutil.Call(t, testHandler.ReplyRequirement, request(testUserID, "reply-wrong-target", input)).Want(http.StatusForbidden)
	stale := input
	stale.ExpectedIssueRevision--
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-stale", stale)).Want(http.StatusConflict)
	wrong := input
	wrong.QuestionCommentID = "00000000-0000-4000-8000-000000000099"
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-wrong-question", wrong)).Want(http.StatusConflict)
	taskRequest := request(testUserID, "reply-task", input)
	taskRequest.Header.Set("X-Actor-Source", "task_token")
	taskRequest.Header.Set("X-Agent-ID", f.AgentID)
	taskRequest.Header.Set("X-Task-ID", taskID)
	testutil.Call(t, testHandler.ReplyRequirement, taskRequest).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "", input)).Want(http.StatusBadRequest)
	var replied service.RequirementReplyResponse
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-target-success", input)).Want(http.StatusCreated).JSON(&replied)
	if replied.Reply.AuthorID != viewer || replied.Reply.QuestionCommentID != input.QuestionCommentID || replied.Reply.IssueRevision != input.ExpectedIssueRevision+1 {
		t.Fatalf("reply=%+v", replied.Reply)
	}
	changed := input
	changed.Content = "Different retry"
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-target-success", changed)).Want(http.StatusConflict)
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-same-body-new-key", input)).Want(http.StatusConflict)
	var replacement requirementBlockResponse
	testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, taskID, "Which fallback?", viewer, replied.Reply.IssueRevision, "reply-replacement-question")).Want(http.StatusOK).JSON(&replacement)
	oldQuestion := input
	oldQuestion.ExpectedIssueRevision = replacement.QuestionComment.IssueRevision
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-fenced-question", oldQuestion)).Want(http.StatusConflict)
	// Even an identical replay loses read access after participant revocation.
	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id=$1 AND user_id=$2`, f.Detail.ID, viewer)
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-target-success", input)).Want(http.StatusForbidden)
	dbfx.Exec(t, `INSERT INTO requirement_participant(workspace_id,requirement_id,user_id,role) VALUES ($1,$2,$3,'viewer')`, testWorkspaceID, f.Detail.ID, viewer)
	dbfx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, viewer)
	testutil.Call(t, testHandler.ReplyRequirement, request(viewer, "reply-target-success", input)).Want(http.StatusNotFound)
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='member'`, f.Issue.ID); got != 1 {
		t.Fatalf("unauthorized reply persisted: %d", got)
	}
}

func TestRequirementExecutionReplyDuringRecoveryIsRetained(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementBlockFixture(t, "98320")
	blockingTask := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTask, "Need environment", "reply-during-block")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, blockingTask)
	answer := func(content, key string, revision int64) service.RequirementReplyResponse {
		var out service.RequirementReplyResponse
		testutil.Call(t, testHandler.ReplyRequirement, requirementActionRequest(testUserID, "replies", f.Detail.ID, service.ReplyRequirementRequest{ExpectedIssueRevision: revision, QuestionCommentID: blocked.QuestionComment.ID, Content: content}, key)).Want(http.StatusCreated).JSON(&out)
		return out
	}
	first := answer("Use staging", "reply-during-first", blocked.QuestionComment.IssueRevision)
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 1 {
		t.Fatalf("first recovery runs=%d", len(runs))
	}
	firstRecovery := runs[0]
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, firstRecovery.ID)
	if wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID); wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("expected triggered question: %+v", wakeup)
	}
	second := answer("Use the Linux staging host", "reply-during-second", first.Reply.IssueRevision)
	answer("Use the Linux staging host", "reply-during-second", first.Reply.IssueRevision)
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1 AND processed_at IS NULL`, blocked.Wakeup.ID); got != 1 {
		t.Fatalf("pending replies during recovery=%d", got)
	}
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	if got := len(requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)); got != 1 {
		t.Fatalf("parallel recovery tasks=%d", got)
	}
	completeRequirementDelegationTask(t, firstRecovery)
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs = requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 2 {
		t.Fatalf("uncovered second reply lost: %d recovery runs", len(runs))
	}
	var covered int
	for _, run := range runs {
		if uuidToString(run.TriggerCommentID) == second.Reply.ID {
			covered++
		}
	}
	if covered != 1 {
		t.Fatalf("second reply covered %d times", covered)
	}
}

func TestRequirementExecutionRecoveryDoesNotPromoteWorkerToLeader(t *testing.T) {
	enableRequirementFlowForTest(t)
	for _, guest := range []bool{false, true} {
		t.Run(fmt.Sprint(guest), func(t *testing.T) {
			_, leaderID := newRequirementClaudeAgent(t, "Recovery role leader")
			_, workerID := newRequirementClaudeAgent(t, "Recovery role worker")
			squadID := dbfx.Squad(t, "Recovery role squad", leaderID)
			if !guest {
				dbfx.SquadMember(t, squadID, "agent", workerID)
			}
			key := fmt.Sprintf("reply-role-%v", guest)
			detail := createRequirementDelegationForTest(t, requirementDelegationInput(key, squadID), key+"-create")
			approved := approveRequirementDelegationForTest(t, detail, key+"-approve")
			issue := approveIssueForTest(t, approved)
			leader := startRequirementDelegationTask(t, leaderID)
			postRequirementDelegationComment(t, issue.ID, leader, fmt.Sprintf("[@Worker](mention://agent/%s) Implement the fix.", workerID))
			completeRequirementDelegationTask(t, leader)
			worker := startRequirementDelegationTask(t, workerID)
			issueRow, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
			if err != nil {
				t.Fatal(err)
			}
			f := requirementReviewFixture{requirementApproveFixture: requirementApproveFixture{Detail: approved.Requirement, AgentID: workerID}, Issue: issueRow}
			blocked := blockRequirementForTest(t, f, uuidToString(worker.ID), "Need worker input", key+"-block")
			testutil.Call(t, testHandler.ReplyRequirement, requirementActionRequest(testUserID, "replies", detail.ID, service.ReplyRequirementRequest{ExpectedIssueRevision: blocked.QuestionComment.IssueRevision, QuestionCommentID: blocked.QuestionComment.ID, Content: "Use staging"}, key+"-answer")).Want(http.StatusCreated)
			completeRequirementDelegationTask(t, worker)
			if err := tickRequirementRecoveryForTest(t); err != nil {
				t.Fatal(err)
			}
			recovery := startRequirementDelegationTask(t, workerID)
			if recovery.IsLeaderTask || recovery.SquadID != worker.SquadID || recovery.DelegatedFromTaskID != worker.ID {
				t.Fatalf("worker recovery role changed: leader=%v squad=%s parent=%s", recovery.IsLeaderTask, uuidToString(recovery.SquadID), uuidToString(recovery.DelegatedFromTaskID))
			}
			testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, recovery, "in_review")).Want(http.StatusConflict)
		})
	}
}

func TestRequirementExecutionReplyCommitFailureAndLostResponse(t *testing.T) {
	enableRequirementFlowForTest(t)
	for i, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			f := newRequirementBlockFixture(t, fmt.Sprint(98310+i))
			blocked := blockRequirementForTest(t, f, newRequirementBlockTask(t, f), "Need environment", fmt.Sprintf("reply-failure-block-%d", i))
			input := service.ReplyRequirementRequest{ExpectedIssueRevision: blocked.QuestionComment.IssueRevision, QuestionCommentID: blocked.QuestionComment.ID, Content: "Staging"}
			key := fmt.Sprintf("reply-failure-%d", i)
			request := func() *http.Request { return requirementActionRequest(testUserID, "replies", f.Detail.ID, input, key) }
			previous := testHandler.TxStarter
			t.Cleanup(func() { testHandler.TxStarter = previous })
			testHandler.TxStarter = requirementCommitStarter{pool: testPool, commit: commit, injectedErr: errors.New("injected reply commit outcome")}
			testutil.Call(t, testHandler.ReplyRequirement, request()).Want(http.StatusInternalServerError)
			testHandler.TxStarter = previous
			want := 0
			if commit {
				want = 1
			}
			for _, query := range []string{`SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='member'`, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id=$1) AND event_type='comment.created'`, `SELECT count(*) FROM requirement_action WHERE requirement_id=(SELECT id FROM requirement WHERE issue_id=$1) AND action='reply'`, `SELECT count(*) FROM requirement_event WHERE requirement_id=(SELECT id FROM requirement WHERE issue_id=$1) AND action='reply'`} {
				if got := dbfx.Count(t, query, f.Issue.ID); got != want {
					t.Fatalf("atomicity count=%d want=%d for %s", got, want, query)
				}
			}
			testutil.Call(t, testHandler.ReplyRequirement, request()).Want(http.StatusCreated)
			if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='member'`, f.Issue.ID); got != 1 {
				t.Fatalf("retry replies=%d", got)
			}
		})
	}
}
