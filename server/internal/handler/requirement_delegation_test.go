package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func requirementDelegationInput(externalRef, squadID string) service.CreateRequirementDelegationRequest {
	return service.CreateRequirementDelegationRequest{
		ExternalRef: externalRef,
		SquadID:     squadID,
		Candidate: service.RequirementDelegationCandidateInput{
			Goal:               "Delegate a squad requirement",
			NonGoals:           []string{"Do not start an agent before approval"},
			Scope:              []string{"Create and review one squad issue"},
			Constraints:        []string{"Keep execution with the selected squad"},
			AcceptanceCriteria: []string{"A human can review the squad result"},
			ProjectID:          nil,
		},
	}
}

func createRequirementDelegationForTest(t *testing.T, input service.CreateRequirementDelegationRequest, key string) service.RequirementDetail {
	t.Helper()
	response := testutil.Call(t, testHandler.CreateRequirementDelegation, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirement-delegations", input, key, "id", testWorkspaceID,
	)).Want(http.StatusCreated)
	var detail service.RequirementDetail
	response.JSON(&detail)
	registerRequirementCleanup(t, detail.ID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE requirement_id = $1`, detail.ID)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE origin_type = 'requirement_delegation' AND origin_id = $1`, detail.ID)
	return detail
}

func requirementDelegationIssueRevision(t *testing.T, issueID string) int64 {
	t.Helper()
	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM issue WHERE id = $1`, issueID).Scan(&revision)
	return revision
}

func approveRequirementDelegationForTest(t *testing.T, detail service.RequirementDetail, key string) requirementApproveResponse {
	t.Helper()
	input := service.ApproveRequirementRequest{
		ExpectedRevision: detail.Revision,
		CandidateID:      detail.LatestCandidate.ID,
		CandidateDigest:  detail.LatestCandidate.Digest,
	}
	response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, detail.ID, input, key)).Want(http.StatusOK)
	var out requirementApproveResponse
	response.JSON(&out)
	return out
}

func TestCreateRequirementDelegationWaitsForHumanApproval(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Requirement delegation leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Requirement delegation squad", leaderID)
	input := requirementDelegationInput("opencode-create-1", squadID)
	detail := createRequirementDelegationForTest(t, input, "delegation-create-1")

	if detail.State != "awaiting_approval" || detail.IntakeState != "awaiting_approval" || detail.Revision != 2 {
		t.Fatalf("delegation state = %+v", detail)
	}
	if detail.SquadID == nil || *detail.SquadID != squadID || detail.AgentID == nil || *detail.AgentID != leaderID {
		t.Fatalf("delegation assignment = %+v", detail)
	}
	if detail.IssueID != nil || detail.ChatSessionID != nil || detail.LatestCandidate == nil || detail.LatestCandidate.CreatedByTaskID != nil {
		t.Fatalf("delegation started work before approval = %+v", detail)
	}
	if detail.Permissions.CanOpen || detail.Permissions.CanSubmitCandidate || detail.Permissions.CanReconcile {
		t.Fatal("native delegation must not open a chat or replace its candidate through the plan-task API")
	}
	testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
		map[string]any{"expectedRevision": detail.Revision}, "delegation-open-forbidden", "id", testWorkspaceID, "requirementId", detail.ID,
	)).Want(http.StatusConflict)
	var snapshot service.RequirementCandidateSnapshot
	response := testutil.Call(t, testHandler.CreateRequirementDelegation, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirement-delegations", input, "delegation-create-1", "id", testWorkspaceID,
	)).Want(http.StatusCreated)
	var replay service.RequirementDetail
	response.JSON(&replay)
	if replay.ID != detail.ID {
		t.Fatalf("idempotent replay created %q, want %q", replay.ID, detail.ID)
	}
	if err := json.Unmarshal(detail.LatestCandidate.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode delegation candidate: %v", err)
	}
	if snapshot.AgentID != leaderID {
		t.Fatalf("candidate assignment = %+v", snapshot)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1`, detail.ID); got != 0 {
		t.Fatalf("delegation created %d runs before approval", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue WHERE origin_type = 'requirement_delegation' AND origin_id = $1`, detail.ID); got != 0 {
		t.Fatalf("delegation created %d issues before approval", got)
	}

	changed := input
	changed.Candidate.Goal = "A different request"
	testutil.Call(t, testHandler.CreateRequirementDelegation, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirement-delegations", changed, "delegation-create-1", "id", testWorkspaceID,
	)).Want(http.StatusConflict)
}

func TestApproveRequirementDelegationCreatesSquadIssueAndLeaderRun(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Requirement approve delegation leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Requirement approve delegation squad", leaderID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-approve-1", squadID), "delegation-approve-create-1")
	approved := approveRequirementDelegationForTest(t, detail, "delegation-approve-1")
	issue := approveIssueForTest(t, approved)

	if issue.AssigneeType == nil || *issue.AssigneeType != "squad" || issue.AssigneeID == nil || *issue.AssigneeID != squadID {
		t.Fatalf("approved issue assignment = %+v", issue)
	}
	if issue.OriginType == nil || *issue.OriginType != "requirement_delegation" || issue.OriginID == nil || *issue.OriginID != detail.ID {
		t.Fatalf("approved issue origin = %+v", issue)
	}
	if approved.Requirement.State != "executing" || approved.Requirement.SquadID == nil || *approved.Requirement.SquadID != squadID {
		t.Fatalf("approved requirement = %+v", approved.Requirement)
	}
	var taskAgentID, taskSquadID, taskStatus string
	var leaderTask bool
	dbfx.QueryRow(t, `SELECT agent_id::text, squad_id::text, status, is_leader_task FROM agent_task_queue WHERE requirement_id = $1 AND issue_id = $2 AND mode = 'execute'`, detail.ID, issue.ID).
		Scan(&taskAgentID, &taskSquadID, &taskStatus, &leaderTask)
	if taskAgentID != leaderID || taskSquadID != squadID || taskStatus != "queued" || !leaderTask {
		t.Fatalf("initial squad run = agent %s squad %s status %s leader %v", taskAgentID, taskSquadID, taskStatus, leaderTask)
	}
	var mediaPending, delegationMarker bool
	dbfx.QueryRow(t, `SELECT context->>'channel_issue_media_pending' IS NOT NULL, context->>'requirement_delegation' = 'true' FROM agent_task_queue WHERE requirement_id = $1 AND issue_id = $2`, detail.ID, issue.ID).
		Scan(&mediaPending, &delegationMarker)
	if mediaPending || !delegationMarker {
		t.Fatalf("initial run context: media pending %v delegation %v", mediaPending, delegationMarker)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1`, detail.ID); got != 0 {
		t.Fatalf("native delegation created %d yixiezuo operations", got)
	}
}

func TestRequirementDelegationDeferredLeaderMergesBeforePromotion(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Requirement deferred delegation leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Requirement deferred delegation squad", leaderID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-deferred-1", squadID), "delegation-deferred-create-1")
	svc := testHandler.requirementService()
	actor := service.RequirementActor{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID)}
	result, err := svc.ApproveRequirement(context.Background(), actor, parseUUID(detail.ID), service.ApproveRequirementRequest{
		ExpectedRevision: detail.Revision,
		CandidateID:      detail.LatestCandidate.ID,
		CandidateDigest:  detail.LatestCandidate.Digest,
	}, "delegation-deferred-approve-1", "", buildRequirementApproveForTest)
	if err != nil {
		t.Fatalf("approve delegation: %v", err)
	}
	if len(result.PromoteTasks) != 1 || result.PromoteTasks[0].Status != "deferred" {
		t.Fatalf("deferred leader runs = %+v", result.PromoteTasks)
	}
	task := result.PromoteTasks[0]
	pending, err := testHandler.Queries.HasPendingTaskForIssueAndAgent(context.Background(), db.HasPendingTaskForIssueAndAgentParams{
		IssueID: task.IssueID, AgentID: task.AgentID,
	})
	if err != nil || !pending {
		t.Fatalf("deferred leader pending check = %v, %v", pending, err)
	}
	commentID := dbfx.Comment(t, uuidToString(task.IssueID), "Additional delegation context")
	threadPending, err := testHandler.Queries.HasPendingTaskForIssueAndAgentInThread(context.Background(), db.HasPendingTaskForIssueAndAgentInThreadParams{
		IssueID: task.IssueID, AgentID: task.AgentID, ThreadCommentID: parseUUID(commentID),
	})
	if err != nil || !threadPending {
		t.Fatalf("unclaimed native leader did not accept its first thread: %v, %v", threadPending, err)
	}
	merged, err := testHandler.Queries.MergeCommentIntoPendingTask(context.Background(), db.MergeCommentIntoPendingTaskParams{
		IssueID: task.IssueID, AgentID: task.AgentID, NewTriggerCommentID: parseUUID(commentID),
		NewOriginatorUserID: parseUUID(testUserID), NewAccountableUserID: parseUUID(testUserID),
		NewOriginatorSource:    pgtype.Text{String: "direct_human", Valid: true},
		NewTriggerEvidenceKind: pgtype.Text{String: "comment", Valid: true}, NewTriggerEvidenceRefID: parseUUID(commentID),
	})
	if err != nil || merged.ID != task.ID {
		t.Fatalf("merge into deferred leader = %+v, %v", merged, err)
	}
	bound, err := testHandler.Queries.GetAgentTask(context.Background(), task.ID)
	if err != nil || bound.CommentThreadID != parseUUID(commentID) || bound.RequirementID != task.RequirementID || bound.SquadID != task.SquadID || !bound.IsLeaderTask {
		t.Fatalf("first comment changed the approved squad binding: %+v, %v", bound, err)
	}
	otherCommentID := dbfx.Comment(t, uuidToString(task.IssueID), "An independent thread")
	otherPending, err := testHandler.Queries.HasPendingTaskForIssueAndAgentInThread(context.Background(), db.HasPendingTaskForIssueAndAgentInThreadParams{
		IssueID: task.IssueID, AgentID: task.AgentID, ThreadCommentID: parseUUID(otherCommentID),
	})
	if err != nil || otherPending {
		t.Fatalf("bound native leader matched an unrelated thread: %v, %v", otherPending, err)
	}
	_, err = testHandler.Queries.MergeCommentIntoPendingTask(context.Background(), db.MergeCommentIntoPendingTaskParams{
		IssueID: task.IssueID, AgentID: task.AgentID, NewTriggerCommentID: parseUUID(otherCommentID),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("merged an unrelated thread into a bound native leader: %v", err)
	}
	if err := testHandler.TaskService.PromoteDeferredChannelIssueTask(context.Background(), task.ID); err != nil {
		t.Fatalf("promote deferred leader: %v", err)
	}
}

func TestRequirementDelegationRejectAndAcceptUseSquadWithoutSourceWriteback(t *testing.T) {
	enableRequirementFlowForTest(t)
	leaderID := dbfx.Agent(t, "Requirement review delegation leader", testRuntimeID)
	squadID := dbfx.Squad(t, "Requirement review delegation squad", leaderID)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("opencode-review-1", squadID), "delegation-review-create-1")
	approved := approveRequirementDelegationForTest(t, detail, "delegation-review-approve-1")
	issue := approveIssueForTest(t, approved)

	leader := startRequirementDelegationTask(t, leaderID)
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, leader, "in_review")).Want(http.StatusOK)
	completeRequirementDelegationTask(t, leader)
	rejected := testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", detail.ID,
		service.RejectRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID), Reason: "Needs another squad pass"}, "delegation-reject-1")).Want(http.StatusOK)
	var rejectedDetail service.RequirementDetail
	rejected.JSON(&rejectedDetail)
	if rejectedDetail.State != "executing" {
		t.Fatalf("rejected requirement state = %s", rejectedDetail.State)
	}
	var taskSquadID, originatorUserID string
	var leaderTask bool
	dbfx.QueryRow(t, `SELECT squad_id::text, is_leader_task, originator_user_id::text FROM agent_task_queue WHERE requirement_id = $1 AND mode = 'execute' ORDER BY created_at DESC, id DESC LIMIT 1`, detail.ID).
		Scan(&taskSquadID, &leaderTask, &originatorUserID)
	if taskSquadID != squadID || !leaderTask || originatorUserID != testUserID {
		t.Fatalf("rejection run squad = %s leader = %v originator = %s", taskSquadID, leaderTask, originatorUserID)
	}

	rework := startRequirementDelegationTask(t, leaderID)
	testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, rework, "in_review")).Want(http.StatusOK)
	completeRequirementDelegationTask(t, rework)
	accepted := testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", detail.ID,
		service.AcceptRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID)}, "delegation-accept-1")).Want(http.StatusOK)
	var acceptedDetail service.RequirementDetail
	accepted.JSON(&acceptedDetail)
	if acceptedDetail.State != "done" {
		t.Fatalf("accepted requirement state = %s", acceptedDetail.State)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1`, detail.ID); got != 0 {
		t.Fatalf("native acceptance created %d source writebacks", got)
	}
}
