package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func createNativeRequirementForTest(t *testing.T, input service.CreateNativeRequirementRequest, key string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.CreateNativeRequirement, requirementRequest(testUserID, http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/requirement-requests", input, key, "id", testWorkspaceID)).Want(http.StatusCreated).JSON(&detail)
	registerRequirementCleanup(t, detail.ID)
	if detail.ChatSessionID != nil {
		registerRequirementChatCleanup(t, *detail.ChatSessionID)
	}
	dbfx.Cleanup(t, `DELETE FROM issue WHERE origin_type = 'requirement_delegation' AND origin_id = $1`, detail.ID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE requirement_id = $1`, detail.ID)
	return detail
}

func TestNativeRequirementMessageIsAtomicIdempotentAndInvalidatesCandidate(t *testing.T) {
	enableRequirementFlowForTest(t)
	_, agentID := newRequirementClaudeAgent(t, "Native clarification")
	detail := createNativeRequirementForTest(t, service.CreateNativeRequirementRequest{ExternalRef: "native-clarification", Idea: "Fix login", AgentID: agentID}, "native-clarification-create")
	var planID string
	dbfx.QueryRow(t, `SELECT id::text FROM agent_task_queue WHERE requirement_id=$1`, detail.ID).Scan(&planID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running', started_at=now() WHERE id=$1`, planID)
	candidate := submitCandidateForTest(t, agentID, planID, detail.ID, *detail.ChatSessionID, validCandidateRequest(t, detail, detail.Revision), "native-clarification-candidate")
	input := service.SendRequirementMessageRequest{ExpectedRevision: candidate.Revision, Content: "Only repair expired session renewal; keep sign-out unchanged."}
	request := func(userID, key string, body service.SendRequirementMessageRequest) *http.Request {
		return requirementRequest(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/messages", body, key, "id", testWorkspaceID, "requirementId", detail.ID)
	}
	var replied service.RequirementDetail
	testutil.Call(t, testHandler.SendRequirementMessage, request(testUserID, "native-reply", input)).Want(http.StatusCreated).JSON(&replied)
	if replied.Revision != candidate.Revision+1 || replied.State != "clarifying" {
		t.Fatalf("reply did not invalidate prior candidate: %+v", replied)
	}
	// A lost response can be retried after the revision advanced without enqueueing twice.
	var replay service.RequirementDetail
	testutil.Call(t, testHandler.SendRequirementMessage, request(testUserID, "native-reply", input)).Want(http.StatusCreated).JSON(&replay)
	if replay.Revision != replied.Revision {
		t.Fatalf("replay revision=%d", replay.Revision)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id=$1 AND mode='plan'`, detail.ID); count != 2 {
		t.Fatalf("Plan task count=%d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND role='user' AND content=$2`, *detail.ChatSessionID, input.Content); count != 1 {
		t.Fatalf("reply count=%d", count)
	}
	if count := dbfx.Count(t, `SELECT count(*) FROM chat_message m JOIN agent_task_queue t ON m.task_id=t.id WHERE m.content=$2 AND t.requirement_id=$1 AND t.mode='plan' AND t.chat_input_task_id=t.id`, detail.ID, input.Content); count != 1 {
		t.Fatalf("sealed Plan input count=%d", count)
	}
	testutil.Call(t, testHandler.SendRequirementMessage, request(testUserID, "native-reply-stale", input)).Want(http.StatusConflict)
	changed := input
	changed.Content = "Changed retry"
	testutil.Call(t, testHandler.SendRequirementMessage, request(testUserID, "native-reply", changed)).Want(http.StatusConflict)
	testutil.Call(t, testHandler.ApproveRequirement, approveRequirementRequest(testUserID, detail.ID, service.ApproveRequirementRequest{ExpectedRevision: replied.Revision, CandidateID: candidate.LatestCandidate.ID, CandidateDigest: candidate.LatestCandidate.Digest}, "native-old-candidate")).Want(http.StatusConflict)
	// An already-running Plan captured the old revision and cannot overwrite the clarification.
	testutil.Call(t, testHandler.SubmitRequirementCandidate, candidateRequest(testUserID, agentID, planID, detail.ID, *detail.ChatSessionID, validCandidateRequest(t, detail, candidate.Revision), "native-stale-plan")).Want(http.StatusConflict)
	viewer := dbfx.User(t, "Native viewer", "native-clarification-viewer@multica.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	dbfx.Exec(t, `INSERT INTO requirement_participant (workspace_id, requirement_id, user_id, role) VALUES ($1,$2,$3,'viewer')`, testWorkspaceID, detail.ID, viewer)
	input.ExpectedRevision = replied.Revision
	testutil.Call(t, testHandler.SendRequirementMessage, request(viewer, "native-viewer-reply", input)).Want(http.StatusForbidden)
	if count := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id=$1`, detail.ID); count != 2 {
		t.Fatalf("rejected writes created tasks: %d", count)
	}
	dbfx.Exec(t, `UPDATE requirement SET cancelled_at=now() WHERE id=$1`, detail.ID)
	testutil.Call(t, testHandler.SendRequirementMessage, request(testUserID, "native-cancelled-reply", input)).Want(http.StatusConflict)
}

func TestNativeRequirementListFindsOnlyParticipantRequests(t *testing.T) {
	enableRequirementFlowForTest(t)
	_, agentID := newRequirementClaudeAgent(t, "Native list")
	viewer := dbfx.User(t, "Native list member", "native-list-viewer@multica.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	var created []service.RequirementDetail
	for i := 0; i < 3; i++ {
		created = append(created, createNativeRequirementForTest(t, service.CreateNativeRequirementRequest{ExternalRef: fmt.Sprintf("native-list-%d", i), Idea: "Fix a bug", AgentID: agentID}, fmt.Sprintf("native-list-create-%d", i)))
	}
	list := func(userID, query string) *http.Request {
		return requirementRequest(userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements"+query, nil, "", "id", testWorkspaceID)
	}
	var filtered requirementListResponse
	testutil.Call(t, testHandler.GetRequirementByChat, list(testUserID, "?externalRef=native-list-1")).Want(http.StatusOK).JSON(&filtered)
	if len(filtered.Requirements) != 1 || filtered.Requirements[0].ID != created[1].ID || filtered.HasMore {
		t.Fatalf("external lookup=%+v", filtered)
	}
	var page requirementListResponse
	testutil.Call(t, testHandler.GetRequirementByChat, list(testUserID, "?limit=1")).Want(http.StatusOK).JSON(&page)
	if len(page.Requirements) != 1 || !page.HasMore || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("pagination=%+v", page)
	}
	var next requirementListResponse
	testutil.Call(t, testHandler.GetRequirementByChat, list(testUserID, "?limit=1&offset=1")).Want(http.StatusOK).JSON(&next)
	if len(next.Requirements) != 1 || next.Requirements[0].ID == page.Requirements[0].ID {
		t.Fatalf("next page=%+v", next)
	}
	var hidden requirementListResponse
	testutil.Call(t, testHandler.GetRequirementByChat, list(viewer, "?externalRef=native-list-1")).Want(http.StatusOK).JSON(&hidden)
	if len(hidden.Requirements) != 0 {
		t.Fatalf("non-participant saw requests: %+v", hidden)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?offset=-1", "?offset=2147483647"} {
		testutil.Call(t, testHandler.GetRequirementByChat, list(testUserID, query)).Want(http.StatusBadRequest)
	}
}

func TestRequirementListReturnsSummaryWithoutCandidateOrRunProjection(t *testing.T) {
	enableRequirementFlowForTest(t)
	leader := dbfx.Agent(t, "Summary projection leader", testRuntimeID)
	squad := dbfx.Squad(t, "Summary projection squad", leader)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("summary-projection", squad), "summary-projection-create")
	r := requirementRequest(testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements?externalRef=summary-projection",
		nil, "", "id", testWorkspaceID)
	var page struct {
		Requirements []map[string]json.RawMessage `json:"requirements"`
	}
	testutil.Call(t, testHandler.GetRequirementByChat, r).Want(http.StatusOK).JSON(&page)
	if len(page.Requirements) != 1 {
		t.Fatalf("summary count=%d", len(page.Requirements))
	}
	for _, field := range []string{"latestCandidate", "permissions", "sourceSnapshot", "participants", "issue", "run", "blockedQuestion", "operations", "deliveries"} {
		if _, exists := page.Requirements[0][field]; exists {
			t.Errorf("list unexpectedly built detail field %q", field)
		}
	}
	var summary service.RequirementSummary
	body, err := json.Marshal(page.Requirements[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ID != detail.ID || summary.State != detail.State || summary.Title != "Delegate a squad requirement" || summary.SquadID == nil || *summary.SquadID != squad {
		t.Fatalf("summary lost navigation or state: %+v", summary)
	}
}

func TestNativeRequirementRequestStartsOnlyPlanForAgentAndSquad(t *testing.T) {
	enableRequirementFlowForTest(t)
	for _, target := range []string{"agent", "squad"} {
		t.Run(target, func(t *testing.T) {
			_, agentID := newRequirementClaudeAgent(t, "Native Plan "+target)
			input := service.CreateNativeRequirementRequest{ExternalRef: "native-plan-" + target, Idea: "Fix the login bug  \nPropose verification steps", AgentID: agentID}
			var squadID string
			if target == "squad" {
				squadID = dbfx.Squad(t, "Native Plan squad", agentID)
				input.AgentID, input.SquadID = "", squadID
			}
			detail := createNativeRequirementForTest(t, input, "native-plan-create-"+target)
			if detail.State != "clarifying" || detail.ChatSessionID == nil || detail.IssueID != nil || detail.LatestCandidate != nil {
				t.Fatalf("native intake skipped planning: %+v", detail)
			}
			if detail.AgentID == nil || *detail.AgentID != agentID || (target == "squad" && (detail.SquadID == nil || *detail.SquadID != squadID)) {
				t.Fatalf("native target changed: %+v", detail)
			}
			var planID, mode, status string
			dbfx.QueryRow(t, `SELECT id::text, mode, status FROM agent_task_queue WHERE requirement_id = $1`, detail.ID).Scan(&planID, &mode, &status)
			if mode != "plan" || status != "queued" {
				t.Fatalf("initial task mode=%s status=%s", mode, status)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM issue WHERE origin_type = 'requirement_delegation' AND origin_id = $1`, detail.ID); got != 0 {
				t.Fatalf("created %d issues before approval", got)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND role = 'user' AND content = $2`, *detail.ChatSessionID, input.Idea); got != 1 {
				t.Fatalf("visible intake messages = %d", got)
			}
			var run struct {
				ID   string `json:"id"`
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(detail.Run, &run); err != nil || run.ID != planID || run.Mode != "plan" {
				t.Fatalf("Plan projection=%s, %v", detail.Run, err)
			}
			var replay service.RequirementDetail
			testutil.Call(t, testHandler.CreateNativeRequirement, requirementRequest(testUserID, http.MethodPost,
				"/api/workspaces/"+testWorkspaceID+"/requirement-requests", input, "native-plan-create-"+target, "id", testWorkspaceID)).Want(http.StatusCreated).JSON(&replay)
			if replay.ID != detail.ID {
				t.Fatalf("replay created another requirement: %s", replay.ID)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1`, detail.ID); got != 1 {
				t.Fatalf("replay created %d Plan runs", got)
			}
			opened := openRequirementForTest(t, testUserID, detail.ID, detail.Revision, "native-plan-open-"+target)
			if opened.PendingTask.TaskID != planID {
				t.Fatalf("open duplicated initial Plan: %+v", opened)
			}
			dbfx.Exec(t, `UPDATE agent_task_queue SET status='running', started_at=now() WHERE id=$1`, planID)
			candidate := submitCandidateForTest(t, agentID, planID, detail.ID, *detail.ChatSessionID,
				validCandidateRequest(t, detail, detail.Revision), "native-candidate-"+target)
			if candidate.LatestCandidate == nil || candidate.LatestCandidate.CreatedByTaskID == nil || *candidate.LatestCandidate.CreatedByTaskID != planID {
				t.Fatalf("candidate has no real Plan provenance: %+v", candidate.LatestCandidate)
			}
			legacyRead := requirementRequest(testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID,
				nil, "", "id", testWorkspaceID, "requirementId", detail.ID)
			legacyRead.Header.Del("X-Client-Capabilities")
			var legacyDetail service.RequirementDetail
			testutil.Call(t, testHandler.GetRequirementByID, legacyRead).Want(http.StatusOK).JSON(&legacyDetail)
			if legacyDetail.LatestCandidate == nil || legacyDetail.LatestCandidate.CreatedByTaskID == nil {
				t.Fatal("legacy reader lost a Candidate produced by a real Plan")
			}
			plan, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(planID))
			if err != nil {
				t.Fatal(err)
			}
			completeRequirementDelegationTask(t, plan)
			approved := approveRequirementDelegationForTest(t, candidate, "native-approve-"+target)
			issue := approveIssueForTest(t, approved)
			if issue.AssigneeType == nil || *issue.AssigneeType != target {
				t.Fatalf("approved target=%+v", issue)
			}
			execution := startRequirementDelegationTask(t, agentID)
			if execution.Mode != "execute" || execution.IsLeaderTask != (target == "squad") {
				t.Fatalf("execution role=%+v", execution)
			}
			if target == "squad" {
				testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, execution, "in_review")).Want(http.StatusOK)
			}
			completeRequirementDelegationTask(t, execution)
			assertRequirementDelegationStatus(t, issue.ID, "in_review")
			var rejected service.RequirementDetail
			testutil.Call(t, testHandler.RejectRequirement, requirementActionRequest(testUserID, "reject", detail.ID,
				service.RejectRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID), Reason: "Add expiry regression coverage"}, "native-reject-"+target)).Want(http.StatusOK).JSON(&rejected)
			if rejected.State != "executing" {
				t.Fatalf("rejection state=%s", rejected.State)
			}
			rework := startRequirementDelegationTask(t, agentID)
			if !rework.TriggerCommentID.Valid || rework.CommentThreadID != rework.TriggerCommentID {
				t.Fatalf("rework did not bind the rejection thread: %+v", rework)
			}
			var rejectionReason string
			dbfx.QueryRow(t, `SELECT content FROM comment WHERE id=$1`, rework.TriggerCommentID).Scan(&rejectionReason)
			if rejectionReason != "Add expiry regression coverage" {
				t.Fatalf("rework input=%q", rejectionReason)
			}
			if rework.AgentID != execution.AgentID || rework.RequirementID != execution.RequirementID || rework.IsLeaderTask != execution.IsLeaderTask {
				t.Fatalf("rework changed approved target: %+v", rework)
			}
			if target == "squad" {
				testutil.Call(t, testHandler.UpdateIssue, requirementDelegationStatusRequest(issue.ID, rework, "in_review")).Want(http.StatusOK)
			}
			completeRequirementDelegationTask(t, rework)
			assertRequirementDelegationStatus(t, issue.ID, "in_review")
			testutil.Call(t, testHandler.SendRequirementMessage, requirementActionRequest(testUserID, "messages", detail.ID,
				service.SendRequirementMessageRequest{ExpectedRevision: rejected.Revision, Content: "Late planning input"}, "native-late-message-"+target)).Want(http.StatusConflict)
			var accepted service.RequirementDetail
			testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", detail.ID,
				service.AcceptRequirementRequest{ExpectedIssueRevision: requirementDelegationIssueRevision(t, issue.ID), Summary: "Approved fix verified"}, "native-accept-"+target)).Want(http.StatusOK).JSON(&accepted)
			if accepted.State != "done" {
				t.Fatalf("accepted state=%s", accepted.State)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1`, detail.ID); got != 0 {
				t.Fatalf("native request caused %d source writes", got)
			}
		})
	}
}

func TestNativeRequirementRequestRejectsInvalidTargetsAndParticipants(t *testing.T) {
	enableRequirementFlowForTest(t)
	_, agentID := newRequirementClaudeAgent(t, "Native request validation")
	squadID := dbfx.Squad(t, "Native validation squad", agentID)
	outsider := dbfx.User(t, "Native outsider", "native-plan-outsider@multica.test")
	for _, tc := range []struct {
		name   string
		input  service.CreateNativeRequirementRequest
		status int
	}{
		{"missing target", service.CreateNativeRequirementRequest{ExternalRef: "invalid-none", Idea: "Fix a bug"}, http.StatusBadRequest},
		{"two targets", service.CreateNativeRequirementRequest{ExternalRef: "invalid-two", Idea: "Fix a bug", AgentID: agentID, SquadID: squadID}, http.StatusBadRequest},
		{"malformed target", service.CreateNativeRequirementRequest{ExternalRef: "invalid-id", Idea: "Fix a bug", AgentID: "not-a-uuid"}, http.StatusBadRequest},
		{"missing idea", service.CreateNativeRequirementRequest{ExternalRef: "invalid-idea", AgentID: agentID}, http.StatusBadRequest},
		{"empty sanitized idea", service.CreateNativeRequirementRequest{ExternalRef: "invalid-nul", Idea: "\x00", AgentID: agentID}, http.StatusBadRequest},
		{"non-member approver", service.CreateNativeRequirementRequest{ExternalRef: "invalid-participant", Idea: "Fix a bug", AgentID: agentID, Participants: []service.RequirementParticipantInput{{UserID: outsider, Roles: []string{"approver"}}}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Call(t, testHandler.CreateNativeRequirement, requirementRequest(testUserID, http.MethodPost,
				"/api/workspaces/"+testWorkspaceID+"/requirement-requests", tc.input, "native-invalid-"+tc.name, "id", testWorkspaceID)).Want(tc.status)
			if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE workspace_id=$1 AND external_id=$2`, testWorkspaceID, tc.input.ExternalRef); got != 0 {
				t.Fatalf("invalid request persisted %d requirements", got)
			}
		})
	}
}
