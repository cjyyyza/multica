package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func startRequirementDelegationTask(t *testing.T, agentID string) db.AgentTaskQueue {
	t.Helper()
	ctx := context.Background()
	task, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agentID))
	if err != nil || task == nil {
		t.Fatalf("claim delegation task: %v, %v", task, err)
	}
	if err := testHandler.TaskService.ValidateTaskRequirement(ctx, *task, parseUUID(testWorkspaceID)); err != nil {
		t.Fatalf("validate claimed delegation task: %v", err)
	}
	started, err := testHandler.TaskService.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("start delegation task: %v", err)
	}
	return *started
}

func completeRequirementDelegationTask(t *testing.T, task db.AgentTaskQueue) {
	t.Helper()
	_, transitioned, err := testHandler.TaskService.CompleteTaskWithTransition(context.Background(), task.ID,
		[]byte(`{"output":"Squad turn complete"}`), "", "", "", false, "", "")
	if err != nil || !transitioned {
		t.Fatalf("complete delegation task: transitioned=%v, err=%v", transitioned, err)
	}
}

func requirementDelegationStatusRequest(issueID string, task db.AgentTaskQueue, status string) *http.Request {
	r := withURLParam(newRequest(http.MethodPatch, "/api/issues/"+issueID, map[string]any{"status": status}), "id", issueID)
	r.Header.Set("X-Agent-ID", uuidToString(task.AgentID))
	r.Header.Set("X-Task-ID", uuidToString(task.ID))
	return r
}

func postRequirementDelegationComment(t *testing.T, issueID string, task db.AgentTaskQueue, content string) string {
	t.Helper()
	body := map[string]any{"content": content}
	if task.TriggerCommentID.Valid {
		body["parent_id"] = uuidToString(task.TriggerCommentID)
	}
	r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
	r.Header.Set("X-Agent-ID", uuidToString(task.AgentID))
	r.Header.Set("X-Task-ID", uuidToString(task.ID))
	var out struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.CreateComment, r).Want(http.StatusCreated).JSON(&out)
	return out.ID
}

func assertRequirementDelegationStatus(t *testing.T, issueID, want string) {
	t.Helper()
	var got string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&got)
	if got != want {
		t.Fatalf("requirement issue status = %q, want %q", got, want)
	}
}

func TestRequirementDelegationRejectsNonMemberParticipants(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Delegation participant leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Delegation participant squad", leaderID)
	outsiderID := dbfx.User(t, "Non-member approver", "delegation-outsider@multica.test")
	input := requirementDelegationInput("opencode-outsider-approver", squadID)
	input.Participants = []service.RequirementParticipantInput{{UserID: outsiderID, Roles: []string{"approver"}}}
	testutil.Call(t, testHandler.CreateRequirementDelegation, requirementRequest(testUserID, http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/requirement-delegations", input, "delegation-outsider", "id", testWorkspaceID)).Want(http.StatusBadRequest)
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE workspace_id = $1 AND external_id = $2`, testWorkspaceID, input.ExternalRef); got != 0 {
		t.Fatalf("invalid participants persisted %d requirements", got)
	}
}

func TestRequirementDelegationLeaderTurnDoesNotSubmitForReview(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Delegation dispatch-only leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Delegation dispatch-only squad", leaderID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-dispatch-only", squadID), "delegation-dispatch-only-create")
	issue := approveIssueForTest(t, approveRequirementDelegationForTest(t, detail, "delegation-dispatch-only-approve"))
	leader := startRequirementDelegationTask(t, leaderID)
	completeRequirementDelegationTask(t, leader)
	assertRequirementDelegationStatus(t, issue.ID, "in_progress")
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", detail.ID,
		service.AcceptRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID)}, "delegation-dispatch-only-accept")).Want(http.StatusConflict)
}

func TestRequirementDelegationWorkerScope(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Delegation scope leader", testRuntimeID)
	workerID := dbfx.Agent(t, "Delegation scope member", testRuntimeID)
	guestID := dbfx.Agent(t, "Delegation scope guest", testRuntimeID)
	otherLeaderID := dbfx.Agent(t, "Other delegation leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Delegation scope squad", leaderID)
	otherSquadID := dbfx.Squad(t, "Other delegation squad", otherLeaderID)
	dbfx.SquadMember(t, squadID, "agent", workerID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-worker-scope", squadID), "delegation-scope-create")
	issue := approveIssueForTest(t, approveRequirementDelegationForTest(t, detail, "delegation-scope-approve"))
	var leaderTaskID string
	dbfx.QueryRow(t, `SELECT id::text FROM agent_task_queue WHERE requirement_id = $1`, detail.ID).Scan(&leaderTaskID)
	otherIssueID := dbfx.Issue(t, "Not the approved issue")
	for _, tc := range []struct {
		name    string
		agentID string
		over    testutil.Cols
		allowed bool
	}{
		{"squad member", workerID, nil, true},
		{"delegated guest", guestID, testutil.Cols{"delegated_from_task_id": leaderTaskID}, true},
		{"unrelated agent", guestID, nil, false},
		{"another squad", workerID, testutil.Cols{"squad_id": otherSquadID}, false},
		{"another issue", workerID, testutil.Cols{"issue_id": otherIssueID}, false},
		{"forged leader role", workerID, testutil.Cols{"squad_id": squadID, "is_leader_task": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols := testutil.Cols{"runtime_id": testRuntimeID, "issue_id": issue.ID, "requirement_id": detail.ID, "status": "running", "mode": "execute"}
			for key, value := range tc.over {
				cols[key] = value
			}
			taskID := dbfx.Task(t, tc.agentID, cols)
			task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
			if err != nil {
				t.Fatal(err)
			}
			err = testHandler.TaskService.ValidateTaskRequirement(context.Background(), task, parseUUID(testWorkspaceID))
			if (err == nil) != tc.allowed {
				t.Fatalf("worker scope allowed=%v, err=%v", tc.allowed, err)
			}
			r := requirementRequest(testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID, nil, "", "id", testWorkspaceID, "requirementId", detail.ID)
			r.Header.Set("X-Actor-Source", "task_token")
			r.Header.Set("X-Agent-ID", tc.agentID)
			r.Header.Set("X-Task-ID", taskID)
			r.Header.Set(taskpolicy.HeaderTaskMode, "execute")
			r.Header.Set(taskpolicy.HeaderRequirementID, detail.ID)
			status := http.StatusForbidden
			if tc.allowed {
				status = http.StatusOK
			}
			testutil.Call(t, testHandler.GetRequirementByID, r).Want(status)
		})
	}
}

func TestRequirementDelegationReviewWaitsForChildTasks(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Delegation child review leader", testRuntimeID)
	workerID := dbfx.Agent(t, "Delegation child review worker", testRuntimeID)
	squadID := dbfx.Squad(t, "Delegation child review squad", leaderID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-child-review", squadID), "delegation-child-create")
	issue := approveIssueForTest(t, approveRequirementDelegationForTest(t, detail, "delegation-child-approve"))
	leader := startRequirementDelegationTask(t, leaderID)
	childID := dbfx.Issue(t, "Delegated child", testutil.Cols{"parent_issue_id": issue.ID})
	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory"} {
		t.Run(status, func(t *testing.T) {
			dbfx.Task(t, workerID, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": childID, "status": status})
			testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, leader, "in_review")).Want(http.StatusConflict)
			assertRequirementDelegationStatus(t, issue.ID, "in_progress")
		})
	}
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, leader, "in_review")).Want(http.StatusOK)
}

func TestRequirementDelegationApprovalDispatchReviewAndAcceptance(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Delegation lifecycle leader", testRuntimeID)
	workerID := dbfx.Agent(t, "Delegation lifecycle worker", testRuntimeID)
	squadID := dbfx.Squad(t, "Delegation lifecycle squad", leaderID)
	dbfx.SquadMember(t, squadID, "agent", workerID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-full-lifecycle", squadID), "delegation-lifecycle-create")
	approved := approveRequirementDelegationForTest(t, detail, "delegation-lifecycle-approve")
	issue := approveIssueForTest(t, approved)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
	leader := startRequirementDelegationTask(t, leaderID)
	postRequirementDelegationComment(t, issue.ID, leader,
		fmt.Sprintf("[@Worker](mention://agent/%s) Implement the approved scope and report the verification result here.", workerID))
	var workerTaskID, delegatedFrom string
	dbfx.QueryRow(t, `SELECT id::text, delegated_from_task_id::text FROM agent_task_queue WHERE requirement_id = $1 AND agent_id = $2 AND status = 'queued'`, detail.ID, workerID).Scan(&workerTaskID, &delegatedFrom)
	if delegatedFrom != uuidToString(leader.ID) {
		t.Fatalf("worker delegation parent = %q, want %s", delegatedFrom, uuidToString(leader.ID))
	}
	completeRequirementDelegationTask(t, leader)
	assertRequirementDelegationStatus(t, issue.ID, "in_progress")
	worker := startRequirementDelegationTask(t, workerID)
	if uuidToString(worker.ID) != workerTaskID || worker.RequirementID != parseUUID(detail.ID) {
		t.Fatalf("worker lost approved requirement binding: %+v", worker)
	}
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, worker, "in_review")).Want(http.StatusConflict)
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, worker, "done")).Want(http.StatusConflict)
	postRequirementDelegationComment(t, issue.ID, worker, "Implementation complete. Regression checks passed; please review the deliverable.")
	reviewLeader := startRequirementDelegationTask(t, leaderID)
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, reviewLeader, "in_review")).Want(http.StatusConflict)
	completeRequirementDelegationTask(t, worker)
	assertRequirementDelegationStatus(t, issue.ID, "in_progress")
	postRequirementDelegationComment(t, issue.ID, reviewLeader, "Reviewed the worker result against all approved acceptance criteria. Ready for human acceptance.")
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, reviewLeader, "in_review")).Want(http.StatusOK)
	completeRequirementDelegationTask(t, reviewLeader)
	assertRequirementDelegationStatus(t, issue.ID, "in_review")

	rejected := testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", detail.ID,
		service.RejectRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID), Reason: "Include the final verification receipt"}, "delegation-lifecycle-reject")).Want(http.StatusOK)
	var rejectedDetail service.RequirementDetail
	rejected.JSON(&rejectedDetail)
	if rejectedDetail.State != "executing" {
		t.Fatalf("rejected state = %s", rejectedDetail.State)
	}
	rework := startRequirementDelegationTask(t, leaderID)
	if !rework.IsLeaderTask || rework.SquadID != parseUUID(squadID) {
		t.Fatalf("rework changed squad: %+v", rework)
	}
	postRequirementDelegationComment(t, issue.ID, rework, "Final verification receipt attached; all acceptance criteria confirmed.")
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, rework, "in_review")).Want(http.StatusOK)
	completeRequirementDelegationTask(t, rework)
	acceptInput := service.AcceptRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID), Summary: "Squad delivery and regression evidence reviewed"}
	for range 2 {
		var accepted service.RequirementDetail
		testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", detail.ID, acceptInput, "delegation-lifecycle-accept")).Want(http.StatusOK).JSON(&accepted)
		if accepted.State != "done" {
			t.Fatalf("accepted state = %s", accepted.State)
		}
	}
	assertRequirementDelegationStatus(t, issue.ID, "done")
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1`, detail.ID); got != 0 {
		t.Fatalf("native acceptance created %d writebacks", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_event WHERE requirement_id = $1 AND action = 'accepted'`, detail.ID); got != 1 {
		t.Fatalf("accepted events = %d", got)
	}
}
