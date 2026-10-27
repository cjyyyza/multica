package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementApproveFixture struct {
	Detail         service.RequirementDetail
	Opened         requirementOpenResponse
	AgentID        string
	Source         requirementSourceFixture
	OperationID    string
	InstallationID string
	BindingID      string
}

func newRequirementApproveFixture(t *testing.T, externalID string, projectIDs ...string) requirementApproveFixture {
	t.Helper()
	return newRequirementApproveFixtureWithAttachments(t, externalID, nil, nil, projectIDs...)
}

func newRequirementApproveFixtureWithAttachments(t *testing.T, externalID string, sourceAttachments []yixiezuo.Attachment, candidateAttachmentIDs []string, projectIDs ...string) requirementApproveFixture {
	t.Helper()
	_, agentID := newRequirementClaudeAgent(t, "Requirement approve agent "+externalID)
	source := newRequirementSourceFixture(externalID)
	if sourceAttachments != nil {
		source.Snapshot.Attachments = sourceAttachments
		source.Snapshot.Digest = source.Snapshot.Fingerprint()
		source.Raw = requirementJSON(source.Snapshot)
		source.Digest = source.Snapshot.Digest
	}
	sourceChatID := dbfx.ChatSession(t, agentID)
	installationID := dbfx.Insert(t, "channel_installation", testutil.Cols{
		"workspace_id": testWorkspaceID, "agent_id": agentID, "channel_type": "popo",
		"installer_user_id": testUserID, "status": "active",
	})
	channelChatID := "requirement-approve-" + externalID
	bindingID := dbfx.Insert(t, "channel_chat_session_binding", testutil.Cols{
		"chat_session_id": sourceChatID, "installation_id": installationID, "channel_type": "popo",
		"channel_chat_id": channelChatID, "chat_type": "group", "route_revision": 7,
	})
	operationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "preview", "state": "succeeded",
		"payload": requirementJSON(yixiezuo.OperationPayload{
			Source: source.Source,
			Channel: &yixiezuo.ChannelScope{
				InstallationID: installationID, ChatID: channelChatID, ChatType: "group",
			},
		}),
		"result": source.Raw,
	})
	body := source.body(agentID)
	if len(projectIDs) > 0 {
		body["projectId"] = projectIDs[0]
	}
	body["intakeOperationId"] = operationID
	detail := createRequirementForTest(t, testUserID, body, "approve-create-"+externalID)
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "approve-open-"+externalID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
	submitted := submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID,
		validCandidateRequestWithAttachments(t, opened.Requirement, 2, candidateAttachmentIDs), "approve-candidate-"+externalID)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1`, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_import WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3`, testWorkspaceID, source.Source.Host, source.Source.ID)
	dbfx.Cleanup(t, `DELETE FROM channel_issue_source WHERE installation_id = $1`, installationID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE requirement_id = $1 OR issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $2)`, detail.ID, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1)`, opened.Session.ID)
	return requirementApproveFixture{
		Detail: submitted, Opened: opened, AgentID: agentID, Source: source,
		OperationID: operationID, InstallationID: installationID, BindingID: bindingID,
	}
}

func (f requirementApproveFixture) input() service.ApproveRequirementRequest {
	return service.ApproveRequirementRequest{
		ExpectedRevision: f.Detail.Revision,
		CandidateID:      f.Detail.LatestCandidate.ID,
		CandidateDigest:  f.Detail.LatestCandidate.Digest,
	}
}

func approveRequirementRequest(userID, requirementID string, input service.ApproveRequirementRequest, key string) *http.Request {
	return requirementRequest(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+requirementID+"/approve", input, key,
		"id", testWorkspaceID, "requirementId", requirementID)
}

func approveRequirementForTest(t *testing.T, f requirementApproveFixture, key string) (requirementApproveResponse, string) {
	t.Helper()
	response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), key)).Want(http.StatusOK)
	var out requirementApproveResponse
	response.JSON(&out)
	return out, response.Body.String()
}

func buildRequirementApproveForTest(detail service.RequirementDetail, issue json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(requirementApproveResponse{Requirement: detail, Issue: issue})
}

func approveIssueForTest(t *testing.T, response requirementApproveResponse) IssueResponse {
	t.Helper()
	var issue IssueResponse
	if err := json.Unmarshal(response.Issue, &issue); err != nil {
		t.Fatalf("decode approved issue: %v", err)
	}
	return issue
}

func requirementApproveEntityCounts(t *testing.T, f requirementApproveFixture) (issues, tasks, approvals int) {
	t.Helper()
	issues = dbfx.Count(t, `SELECT count(*) FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1`, f.Opened.Session.ID)
	tasks = dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1 AND mode = 'execute'`, f.Detail.ID)
	approvals = dbfx.Count(t, `SELECT count(*) FROM requirement_approval WHERE requirement_id = $1`, f.Detail.ID)
	return issues, tasks, approvals
}

func assertRequirementApproveRolledBack(t *testing.T, f requirementApproveFixture) {
	t.Helper()
	issues, tasks, approvals := requirementApproveEntityCounts(t, f)
	if issues != 0 || tasks != 0 || approvals != 0 {
		t.Fatalf("approve left entities: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE id = $1 AND issue_id IS NULL`, f.Detail.ID); got != 1 {
		t.Fatal("approve failure bound the requirement issue")
	}
}

func TestApproveRequirementCreatesBoundIssueTaskAndSourceLinks(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementApproveFixture(t, "94001")
	planTaskID := f.Opened.PendingTask.TaskID
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, planTaskID)
	response, _ := approveRequirementForTest(t, f, "approve-success-94001")
	issue := approveIssueForTest(t, response)
	if response.Requirement.Revision != 4 || response.Requirement.State != "executing" || response.Requirement.IssueID == nil || *response.Requirement.IssueID != issue.ID {
		t.Fatalf("approved requirement = %+v", response.Requirement)
	}
	if issue.OriginType == nil || *issue.OriginType != "requirement_chat" || issue.OriginID == nil || *issue.OriginID != f.Opened.Session.ID {
		t.Fatalf("approved issue origin = %+v", issue)
	}
	if issue.AssigneeID == nil || *issue.AssigneeID != f.AgentID || issue.Metadata["yixiezuo_id"] != f.Source.Source.ID {
		t.Fatalf("approved issue assignment/source metadata = %+v", issue)
	}
	if !requirementSameJSON(t, string(response.Requirement.Issue), string(response.Issue)) {
		t.Fatalf("approve projections: requirement.issue=%s issue=%s", response.Requirement.Issue, response.Issue)
	}
	var taskID, mode, taskRequirementID, taskIssueID, taskStatus string
	dbfx.QueryRow(t, `
		SELECT a.initial_task_id, t.mode, t.requirement_id::text, t.issue_id::text, t.status
		FROM requirement_approval a JOIN agent_task_queue t ON t.id = a.initial_task_id
		WHERE a.requirement_id = $1`, f.Detail.ID).Scan(&taskID, &mode, &taskRequirementID, &taskIssueID, &taskStatus)
	if taskID == "" || mode != "execute" || taskRequirementID != f.Detail.ID || taskIssueID != issue.ID || taskStatus != "queued" {
		t.Fatalf("initial task = id=%s mode=%s requirement=%s issue=%s status=%s", taskID, mode, taskRequirementID, taskIssueID, taskStatus)
	}
	var run AgentTaskResponse
	if err := json.Unmarshal(response.Requirement.Run, &run); err != nil || run.ID != taskID || run.Mode != "execute" || run.RequirementID != f.Detail.ID {
		t.Fatalf("approve run projection = %+v, err=%v", run, err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_approval WHERE requirement_id = $1 AND candidate_id = $2 AND candidate_digest = $3 AND approved_by = $4 AND issue_id = $5`,
		f.Detail.ID, f.Detail.LatestCandidate.ID, f.Detail.LatestCandidate.Digest, testUserID, issue.ID); got != 1 {
		t.Fatalf("approval identity rows = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM channel_issue_source WHERE issue_id = $1 AND installation_id = $2 AND binding_id = $3 AND route_revision = 7`, issue.ID, f.InstallationID, f.BindingID); got != 1 {
		t.Fatalf("channel source rows = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE issue_id = $1 AND source_host = $2 AND external_id = $3`, issue.ID, f.Source.Source.Host, f.Source.Source.ID); got != 1 {
		t.Fatalf("source import rows = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_event WHERE requirement_id = $1 AND action = 'approved'`, f.Detail.ID); got != 1 {
		t.Fatalf("approval events = %d", got)
	}
	var planMode, planStatus string
	dbfx.QueryRow(t, `SELECT mode, status FROM agent_task_queue WHERE id = $1`, planTaskID).Scan(&planMode, &planStatus)
	if planMode != "plan" || planStatus != "completed" {
		t.Fatalf("old plan task changed unexpectedly: mode=%s status=%s", planMode, planStatus)
	}
	planTask, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(planTaskID))
	if err != nil {
		t.Fatal(err)
	}
	err = testHandler.TaskService.ValidateTaskRequirement(context.Background(), planTask, parseUUID(testWorkspaceID))
	var gateErr *service.RequirementChatExecutionError
	if !errors.As(err, &gateErr) || gateErr.Code != service.RequirementChatIssueExistsCode {
		t.Fatalf("old plan task remained executable: %v", err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'queued', started_at = NULL, completed_at = NULL WHERE id = $1`, planTaskID)
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.AgentID).Scan(&runtimeID)
	claimRequest := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "requirement-approve-old-plan")
	claimRequest = withURLParam(claimRequest, "runtimeId", runtimeID)
	claimed, _ := claimPlanResponse(t, claimRequest)
	if claimed.ID != "" {
		t.Fatalf("old plan claim delivered work after approval: %+v", claimed)
	}
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, planTaskID).Scan(&planStatus)
	if planStatus != "failed" {
		t.Fatalf("old plan claim status = %s, want failed settlement", planStatus)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_approval WHERE requirement_id = $1 AND issue_id = $2 AND initial_task_id = $3`, f.Detail.ID, issue.ID, taskID); got != 1 {
		t.Fatalf("old plan claim/completion changed approval: %d", got)
	}
	detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.Detail.ID)
	if detail.IssueDeleted || !requirementSameJSON(t, string(detail.Issue), string(response.Issue)) {
		t.Fatalf("current detail projection = %+v", detail)
	}
	var currentRun AgentTaskResponse
	if err := json.Unmarshal(detail.Run, &currentRun); err != nil || currentRun.ID != taskID {
		t.Fatalf("current run projection = %+v, err=%v", currentRun, err)
	}
}

func TestApproveRequirementConcurrentDifferentKeysCreatesOnceAndPromotesIdempotently(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementApproveFixture(t, "94002")
	svc := requirementServiceForTest()
	actor := service.RequirementActor{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID)}
	type outcome struct {
		result service.RequirementActionResult
		err    error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"approve-concurrent-a-94002", "approve-concurrent-b-94002"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := svc.ApproveRequirement(context.Background(), actor, parseUUID(f.Detail.ID), f.input(), key, "", buildRequirementApproveForTest)
			results <- outcome{result: result, err: err}
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	var promoted []db.AgentTaskQueue
	for result := range results {
		if result.err == nil {
			successes++
			promoted = result.result.PromoteTasks
			continue
		}
		var requirementErr *service.RequirementError
		if !errors.As(result.err, &requirementErr) || requirementErr.Status != http.StatusConflict {
			t.Fatalf("unexpected concurrent approve error: %v", result.err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 || len(promoted) != 1 {
		t.Fatalf("concurrent outcomes: successes=%d conflicts=%d promoted=%d", successes, conflicts, len(promoted))
	}
	for i := 0; i < 2; i++ {
		if err := testHandler.TaskService.PromoteDeferredChannelIssueTask(context.Background(), promoted[0].ID); err != nil {
			t.Fatalf("promote %d: %v", i, err)
		}
	}
	issues, tasks, approvals := requirementApproveEntityCounts(t, f)
	if issues != 1 || tasks != 1 || approvals != 1 {
		t.Fatalf("concurrent entities: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'queued'`, promoted[0].ID); got != 1 {
		t.Fatalf("promoted task status count = %d", got)
	}
}

func TestApproveRequirementReplayHashAndRevokedParticipant(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementApproveFixture(t, "94003")
	_, original := approveRequirementForTest(t, f, "approve-replay-94003")
	var issueID string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM requirement WHERE id = $1`, f.Detail.ID).Scan(&issueID)
	dbfx.Exec(t, `UPDATE issue SET status = 'in_review' WHERE id = $1`, issueID)
	prefixed := f.input()
	prefixed.CandidateDigest = "sha256:" + prefixed.CandidateDigest
	replay := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, prefixed, "approve-replay-94003")).Want(http.StatusOK)
	if !requirementSameJSON(t, original, replay.Body.String()) {
		t.Fatalf("same-key replay changed response:\noriginal=%s\nreplay=%s", original, replay.Body)
	}
	changed := f.input()
	changed.CandidateDigest = "different-request"
	conflict := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, changed, "approve-replay-94003")).Want(http.StatusConflict)
	if conflict.Map()["code"] != "requirement_action_conflict" {
		t.Fatalf("different hash response = %s", conflict.Body)
	}
	issues, tasks, approvals := requirementApproveEntityCounts(t, f)
	if issues != 1 || tasks != 1 || approvals != 1 {
		t.Fatalf("replay created entities: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
	}
	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id = $1 AND user_id = $2`, f.Detail.ID, testUserID)
	testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-replay-94003")).Want(http.StatusForbidden)
	if issues, tasks, approvals = requirementApproveEntityCounts(t, f); issues != 1 || tasks != 1 || approvals != 1 {
		t.Fatalf("revoked replay changed entities: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
	}
}

func TestApproveRequirementRejectsUnauthorizedAndStaleRequests(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("non-approver", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94004")
		other := dbfx.User(t, "Requirement approve viewer", uuid.NewString()+"@example.test")
		dbfx.Member(t, testWorkspaceID, other, "member")
		dbfx.Insert(t, "requirement_participant", testutil.Cols{"workspace_id": testWorkspaceID, "requirement_id": f.Detail.ID, "user_id": other, "role": "viewer"})
		testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(other, f.Detail.ID, f.input(), "approve-viewer-94004")).Want(http.StatusForbidden)
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("member removed", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94005")
		other := dbfx.User(t, "Requirement approve removed member", uuid.NewString()+"@example.test")
		memberID := dbfx.Member(t, testWorkspaceID, other, "member")
		dbfx.Insert(t, "requirement_participant", testutil.Cols{"workspace_id": testWorkspaceID, "requirement_id": f.Detail.ID, "user_id": other, "role": "approver"})
		dbfx.Exec(t, `DELETE FROM member WHERE id = $1`, memberID)
		_, err := requirementServiceForTest().ApproveRequirement(context.Background(), service.RequirementActor{
			WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(other),
		}, parseUUID(f.Detail.ID), f.input(), "approve-removed-94005", "", buildRequirementApproveForTest)
		var requirementErr *service.RequirementError
		if !errors.As(err, &requirementErr) || requirementErr.Status != http.StatusForbidden {
			t.Fatalf("removed member error = %v", err)
		}
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("private agent invocation revoked", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94006")
		other := dbfx.User(t, "Requirement approve private owner", uuid.NewString()+"@example.test")
		dbfx.Exec(t, `UPDATE agent SET owner_id = $2, permission_mode = 'private', visibility = 'private' WHERE id = $1`, f.AgentID, other)
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-private-94006")).Want(http.StatusForbidden)
		if response.Map()["code"] != "agent_invocation_not_allowed" {
			t.Fatalf("private agent response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("task token", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94007")
		req := approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-task-token-94007")
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", f.AgentID)
		req.Header.Set("X-Task-ID", f.Opened.PendingTask.TaskID)
		testutil.Call(t, testHandler.ApproveRequirement, req).Want(http.StatusForbidden)
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("stale revision and candidate", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94008")
		stale := f.input()
		stale.ExpectedRevision = 2
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, stale, "approve-stale-94008")).Want(http.StatusConflict)
		if response.Map()["code"] != "revision_conflict" || response.Map()["actual_revision"] != float64(3) {
			t.Fatalf("stale response = %s", response.Body)
		}
		wrongCandidate := f.input()
		wrongCandidate.CandidateID = uuid.NewString()
		response = testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, wrongCandidate, "approve-wrong-candidate-94008")).Want(http.StatusBadRequest)
		if response.Map()["code"] != "requirement_candidate_not_latest" {
			t.Fatalf("candidate response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
	})
}

func TestApproveRequirementCancelledExistingAndDeletedIssue(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("missing permanent chat", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94019")
		dbfx.Exec(t, `DELETE FROM chat_session WHERE id = $1`, f.Opened.Session.ID)
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-chat-94019")).Want(http.StatusConflict)
		if response.Map()["code"] != "requirement_open_incomplete" {
			t.Fatalf("missing chat response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("cancelled", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94009")
		dbfx.Exec(t, `UPDATE requirement SET cancelled_at = now() WHERE id = $1`, f.Detail.ID)
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-cancelled-94009")).Want(http.StatusConflict)
		if response.Map()["code"] != service.RequirementCancelledCode {
			t.Fatalf("cancelled response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("already bound", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94010")
		existing := dbfx.Issue(t, "Existing requirement issue")
		dbfx.Exec(t, `UPDATE requirement SET issue_id = $2 WHERE id = $1`, f.Detail.ID, existing)
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-existing-94010")).Want(http.StatusConflict)
		if response.Map()["code"] != service.RequirementChatIssueExistsCode {
			t.Fatalf("existing response = %s", response.Body)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM requirement_approval WHERE requirement_id = $1`, f.Detail.ID); got != 0 {
			t.Fatalf("approvals for existing binding = %d", got)
		}
	})
	t.Run("deleted issue tombstone", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94011")
		response, _ := approveRequirementForTest(t, f, "approve-tombstone-original-94011")
		issue := approveIssueForTest(t, response)
		dbfx.Exec(t, `DELETE FROM issue WHERE id = $1`, issue.ID)
		_, tasksBefore, approvalsBefore := requirementApproveEntityCounts(t, f)
		detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.Detail.ID)
		if !detail.IssueDeleted || detail.State != "cancelled" || string(detail.Issue) != "null" || string(detail.Run) != "null" {
			t.Fatalf("tombstone detail = %+v", detail)
		}
		input := f.input()
		input.ExpectedRevision = 4
		second := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, input, "approve-tombstone-second-94011")).Want(http.StatusConflict)
		if second.Map()["code"] != service.RequirementChatIssueExistsCode {
			t.Fatalf("tombstone recreate response = %s", second.Body)
		}
		issues, tasks, approvals := requirementApproveEntityCounts(t, f)
		if issues != 0 || tasks != tasksBefore || approvals != approvalsBefore {
			t.Fatalf("tombstone recreated entities: issues=%d tasks=%d/%d approvals=%d/%d", issues, tasks, tasksBefore, approvals, approvalsBefore)
		}
	})
}

func TestApproveRequirementSourceAssociationFailuresRollBack(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name       string
		externalID string
		prepare    func(*testing.T, requirementApproveFixture)
	}{
		{name: "missing operation", externalID: "94012", prepare: func(t *testing.T, f requirementApproveFixture) {
			dbfx.Exec(t, `UPDATE requirement SET intake_operation_id = NULL WHERE id = $1`, f.Detail.ID)
		}},
		{name: "missing current binding", externalID: "94013", prepare: func(t *testing.T, f requirementApproveFixture) {
			dbfx.Exec(t, `DELETE FROM channel_chat_session_binding WHERE id = $1`, f.BindingID)
		}},
		{name: "forged snapshot digest", externalID: "94014", prepare: func(t *testing.T, f requirementApproveFixture) {
			dbfx.Exec(t, `UPDATE yixiezuo_operation SET result = jsonb_set(result, '{digest}', '"forged"') WHERE id = $1`, f.OperationID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementApproveFixture(t, tc.externalID)
			tc.prepare(t, f)
			response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-source-"+tc.externalID)).Want(http.StatusConflict)
			if response.Map()["code"] != service.RequirementSourceLinkUnavailableCode {
				t.Fatalf("source failure response = %s", response.Body)
			}
			assertRequirementApproveRolledBack(t, f)
		})
	}
	t.Run("channel source insert missing", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94015")
		name := "requirement_approve_skip_channel_94015"
		if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
			CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fn$
			BEGIN
				IF NEW.installation_id = '%s'::uuid THEN RETURN NULL; END IF;
				RETURN NEW;
			END;
			$fn$`, name, f.InstallationID)); err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON channel_issue_source FOR EACH ROW EXECUTE FUNCTION %s()`, name, name)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON channel_issue_source`, name))
			_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
		})
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-source-94015")).Want(http.StatusConflict)
		if response.Map()["code"] != service.RequirementSourceLinkUnavailableCode {
			t.Fatalf("channel insert failure response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("source import conflict", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94016")
		otherIssue := dbfx.Issue(t, "Conflicting source import")
		dbfx.Insert(t, "yixiezuo_import", testutil.Cols{
			"workspace_id": testWorkspaceID, "issue_id": otherIssue, "source_host": f.Source.Source.Host,
			"external_id": f.Source.Source.ID, "source_url": f.Source.Source.URL,
			"snapshot": f.Source.Raw, "imported_by": testUserID,
		})
		response := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-source-94016")).Want(http.StatusConflict)
		if response.Map()["code"] != service.RequirementSourceLinkUnavailableCode {
			t.Fatalf("source import failure response = %s", response.Body)
		}
		assertRequirementApproveRolledBack(t, f)
		if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE issue_id = $1`, otherIssue); got != 1 {
			t.Fatalf("existing source import changed: %d", got)
		}
	})
}

func TestApproveRequirementFromStandalonePreviewHasNoChannelDependency(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementApproveFixture(t, "94031")
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET payload = payload - 'channel' WHERE id = $1`, f.OperationID)
	dbfx.Exec(t, `DELETE FROM channel_chat_session_binding WHERE id = $1`, f.BindingID)
	dbfx.Exec(t, `DELETE FROM channel_installation WHERE id = $1`, f.InstallationID)
	out, _ := approveRequirementForTest(t, f, "approve-standalone-94031")
	issue := approveIssueForTest(t, out)
	if issue.ID == "" || out.Requirement.State != "executing" {
		t.Fatalf("standalone approval = %+v", out)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM channel_issue_source WHERE issue_id = $1`, issue.ID); got != 0 {
		t.Fatalf("standalone approval created %d channel source links", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE issue_id = $1 AND source_host = $2 AND external_id = $3`, issue.ID, f.Source.Source.Host, f.Source.Source.ID); got != 1 {
		t.Fatalf("standalone source import count = %d", got)
	}
	if out.Requirement.IssueID == nil || *out.Requirement.IssueID != issue.ID {
		t.Fatalf("standalone requirement issue link = %+v", out.Requirement.IssueID)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE requirement_id = $1`, f.Detail.ID)
	dbfx.Exec(t, `UPDATE issue SET status = 'in_review', revision = revision + 1 WHERE id = $1`, issue.ID)
	row, err := testHandler.Queries.GetIssue(t.Context(), parseUUID(issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.Detail = out.Requirement
	accepted := acceptRequirementForTest(t, requirementReviewFixture{requirementApproveFixture: f, Issue: row}, "accept-standalone-94031")
	if accepted.State != "done" {
		t.Fatalf("standalone acceptance state = %q", accepted.State)
	}
	var payloadRaw []byte
	dbfx.QueryRow(t, `SELECT payload FROM yixiezuo_operation WHERE requirement_id = $1 AND kind = 'publish'`, f.Detail.ID).Scan(&payloadRaw)
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil || payload.Channel != nil || payload.Source.ID != f.Source.Source.ID {
		t.Fatalf("standalone publication lost its source: %s (%v)", payloadRaw, err)
	}
}

func TestApproveRequirementCommitAtomicityAndResponseReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("commit failure", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94017")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: errors.New("injected approve commit failure")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-commit-94017")).Want(http.StatusInternalServerError)
		testHandler.TxStarter = previous
		assertRequirementApproveRolledBack(t, f)
	})
	t.Run("committed response loss", func(t *testing.T) {
		f := newRequirementApproveFixture(t, "94018")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, commit: true, injectedErr: errors.New("injected approve response loss")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-response-loss-94018")).Want(http.StatusInternalServerError)
		testHandler.TxStarter = previous
		issues, tasks, approvals := requirementApproveEntityCounts(t, f)
		if issues != 1 || tasks != 1 || approvals != 1 {
			t.Fatalf("committed entities missing: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
		}
		response, _ := approveRequirementForTest(t, f, "approve-response-loss-94018")
		if response.Requirement.IssueID == nil || len(response.Issue) == 0 || string(response.Issue) == "null" {
			t.Fatalf("stored response was not replayed: %+v", response)
		}
		conflict := testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-response-loss-new-94018")).Want(http.StatusConflict)
		if conflict.Map()["code"] != "revision_conflict" {
			t.Fatalf("new key after response loss = %s", conflict.Body)
		}
		issues, tasks, approvals = requirementApproveEntityCounts(t, f)
		if issues != 1 || tasks != 1 || approvals != 1 {
			t.Fatalf("response replay duplicated entities: issues=%d tasks=%d approvals=%d", issues, tasks, approvals)
		}
	})
}
