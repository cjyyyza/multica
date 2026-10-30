package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func validCandidateRequest(t *testing.T, detail service.RequirementDetail, revision int64) map[string]any {
	t.Helper()
	return validCandidateRequestWithAttachments(t, detail, revision, nil)
}

func validCandidateRequestWithAttachments(t *testing.T, detail service.RequirementDetail, revision int64, attachmentIDs []string) map[string]any {
	t.Helper()
	if detail.AgentID == nil {
		t.Fatal("requirement has no agent")
	}
	snapshot := map[string]any{
		"goal":               "Ship requirement flow",
		"nonGoals":           []string{"No approval or issue creation"},
		"scope":              []string{"Open and candidate"},
		"constraints":        []string{"Keep task policy"},
		"acceptanceCriteria": []string{"One chat and one plan task"},
		"projectId":          detail.ProjectID,
		"agentId":            *detail.AgentID,
	}
	if attachmentIDs != nil {
		snapshot["attachmentIds"] = attachmentIDs
	}
	return map[string]any{"expectedRevision": revision, "snapshot": snapshot}
}

func candidateSnapshotDigest(t *testing.T, detail service.RequirementDetail, snapshot any) string {
	t.Helper()
	row, err := testHandler.Queries.GetRequirement(context.Background(), db.GetRequirementParams{
		ID: parseUUID(detail.ID), WorkspaceID: parseUUID(detail.WorkspaceID),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, digest, err := service.CanonicalRequirementCandidateSnapshot(requirementJSON(snapshot), row)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func candidateRequest(userID, agentID, taskID, requirementID, chatSessionID string, body map[string]any, key string) *http.Request {
	req := requirementRequest(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+requirementID+"/candidates", body, key,
		"id", testWorkspaceID, "requirementId", requirementID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set(taskpolicy.HeaderTaskMode, taskpolicy.ModePlan)
	req.Header.Set(taskpolicy.HeaderRequirementID, requirementID)
	req.Header.Set(taskpolicy.HeaderChatSessionID, chatSessionID)
	return req
}

func submitCandidateForTest(t *testing.T, agentID, taskID, requirementID, chatSessionID string, body map[string]any, key string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.SubmitRequirementCandidate,
		candidateRequest(testUserID, agentID, taskID, requirementID, chatSessionID, body, key)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func TestSubmitRequirementCandidateSuccessAndReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement candidate agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("93001").body(agentID), "candidate-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "candidate-open")
	taskID := opened.PendingTask.TaskID
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, taskID)
	input := validCandidateRequest(t, opened.Requirement, 2)
	expectedDigest := candidateSnapshotDigest(t, opened.Requirement, input["snapshot"])
	submitted := submitCandidateForTest(t, agentID, taskID, detail.ID, opened.Session.ID, input, "candidate-submit")
	if submitted.Revision != 3 || submitted.State != "awaiting_approval" || submitted.IntakeState != "awaiting_approval" {
		t.Fatalf("candidate state = %+v", submitted)
	}
	if submitted.LatestCandidate == nil || submitted.LatestCandidate.Revision != 3 || submitted.LatestCandidate.CreatedByTaskID == nil || *submitted.LatestCandidate.CreatedByTaskID != taskID {
		t.Fatalf("latest candidate = %+v", submitted.LatestCandidate)
	}
	if submitted.LatestCandidate.Digest != expectedDigest {
		t.Fatalf("candidate digest = %q, want server canonical digest %q", submitted.LatestCandidate.Digest, expectedDigest)
	}
	if !submitted.Permissions.CanSubmitCandidate || submitted.Permissions.CanApprove {
		t.Fatalf("agent candidate permissions = %+v", submitted.Permissions)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_event WHERE requirement_id = $1 AND actor_type = 'agent' AND action = 'candidate_submitted'`, detail.ID); got != 1 {
		t.Fatalf("candidate events = %d, want 1", got)
	}

	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
	replayed := submitCandidateForTest(t, agentID, taskID, detail.ID, opened.Session.ID, input, "candidate-submit")
	if !requirementSameJSON(t, string(requirementJSON(submitted)), string(requirementJSON(replayed))) {
		t.Fatalf("candidate replay changed original detail: %+v", replayed)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_candidate WHERE requirement_id = $1`, detail.ID); got != 1 {
		t.Fatalf("candidate rows after replay = %d, want 1", got)
	}
}

func TestSubmitRequirementCandidateRejectsInvalidTaskAndInput(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	setup := func(externalID string) (service.RequirementDetail, requirementOpenResponse, string) {
		_, agentID := newRequirementClaudeAgent(t, "Requirement invalid candidate agent "+externalID)
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture(externalID).body(agentID), "invalid-candidate-create-"+externalID)
		opened := openRequirementForTest(t, testUserID, detail.ID, 1, "invalid-candidate-open-"+externalID)
		return detail, opened, agentID
	}
	t.Run("not running", func(t *testing.T) {
		detail, opened, agentID := setup("93002")
		input := validCandidateRequest(t, opened.Requirement, 2)
		testutil.Call(t, testHandler.SubmitRequirementCandidate,
			candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-queued")).Want(http.StatusForbidden)
	})
	t.Run("not plan", func(t *testing.T) {
		detail, opened, agentID := setup("93003")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', mode = 'execute', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
		input := validCandidateRequest(t, opened.Requirement, 2)
		testutil.Call(t, testHandler.SubmitRequirementCandidate,
			candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-execute")).Want(http.StatusForbidden)
	})
	t.Run("wrong requirement task", func(t *testing.T) {
		_, openedA, agentA := setup("93008")
		detailB, openedB, _ := setup("93009")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, openedA.PendingTask.TaskID)
		input := validCandidateRequest(t, openedB.Requirement, 2)
		testutil.Call(t, testHandler.SubmitRequirementCandidate,
			candidateRequest(testUserID, agentA, openedA.PendingTask.TaskID, detailB.ID, openedB.Session.ID, input, "candidate-wrong-task")).Want(http.StatusForbidden)
	})
	t.Run("legacy client digest is ignored", func(t *testing.T) {
		detail, opened, agentID := setup("93004")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
		input := validCandidateRequest(t, opened.Requirement, 2)
		input["digest"] = "legacy-client-digest"
		submitted := submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-digest")
		if submitted.LatestCandidate == nil || submitted.LatestCandidate.Digest == "" || submitted.LatestCandidate.Digest == "legacy-client-digest" {
			t.Fatalf("latest candidate digest = %+v", submitted.LatestCandidate)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM requirement_candidate WHERE requirement_id = $1 AND digest = $2`, detail.ID, submitted.LatestCandidate.Digest); got != 1 {
			t.Fatalf("server digest candidates = %d, want 1", got)
		}
	})
	t.Run("stale revision", func(t *testing.T) {
		detail, opened, agentID := setup("93005")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
		submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, validCandidateRequest(t, opened.Requirement, 2), "candidate-stale-original")
		response := testutil.Call(t, testHandler.SubmitRequirementCandidate,
			candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, validCandidateRequest(t, opened.Requirement, 1), "candidate-stale-new")).Want(http.StatusConflict)
		if response.Map()["code"] != "revision_conflict" {
			t.Fatalf("stale response = %s", response.Body)
		}
	})
	t.Run("human actor", func(t *testing.T) {
		detail, opened, agentID := setup("93006")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
		input := validCandidateRequest(t, opened.Requirement, 2)
		req := requirementRequest(testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/candidates", input, "candidate-human",
			"id", testWorkspaceID, "requirementId", detail.ID)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", opened.PendingTask.TaskID)
		req.Header.Set(taskpolicy.HeaderTaskMode, taskpolicy.ModePlan)
		req.Header.Set(taskpolicy.HeaderRequirementID, detail.ID)
		req.Header.Set(taskpolicy.HeaderChatSessionID, opened.Session.ID)
		testutil.Call(t, testHandler.SubmitRequirementCandidate, req).Want(http.StatusForbidden)
	})
}

func TestPlanTaskCanGetOnlyItsRunningScopedRequirement(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement plan get agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("93010").body(agentID), "plan-get-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "plan-get-open")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)

	req := candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, nil, "")
	req.Method = http.MethodGet
	req.URL.Path = "/api/workspaces/" + testWorkspaceID + "/requirements/" + detail.ID
	var got service.RequirementDetail
	testutil.Call(t, testHandler.GetRequirementByID, req).Want(http.StatusOK).JSON(&got)
	if got.ID != detail.ID || got.Revision != 2 || !got.Permissions.CanRead || !got.Permissions.CanSubmitCandidate {
		t.Fatalf("plan task detail = %+v", got)
	}

	other := createRequirementForTest(t, testUserID, newRequirementSourceFixture("93011").body(agentID), "plan-get-other-create")
	wrongScope := candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, nil, "")
	wrongScope.Method = http.MethodGet
	wrongScope.URL.Path = "/api/workspaces/" + testWorkspaceID + "/requirements/" + other.ID
	wrongScope = testutil.WithURLParams(wrongScope, "id", testWorkspaceID, "requirementId", other.ID)
	testutil.Call(t, testHandler.GetRequirementByID, wrongScope).Want(http.StatusForbidden)
}

func TestExecuteTaskCanReadOnlyItsRunningScopedRequirement(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	for index, mutation := range []string{"", "not running", "wrong Issue", "wrong agent", "wrong scope", "unapproved", "cancelled"} {
		name := mutation
		if name == "" {
			name = "read execution context"
		}
		t.Run(name, func(t *testing.T) {
			f := newRequirementBlockFixture(t, fmt.Sprintf("9792%d", index))
			taskID := newRequirementBlockTask(t, f)
			req := requirementBlockRequest(f, taskID, "", testUserID, f.Issue.Revision, "")
			req.Method = http.MethodGet
			req.URL.Path = "/api/workspaces/" + testWorkspaceID + "/requirements/" + f.Detail.ID
			switch mutation {
			case "not running":
				dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, taskID)
			case "wrong Issue":
				dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id = $2 WHERE id = $1`, taskID, dbfx.Issue(t, "Other task issue"))
			case "wrong agent":
				req.Header.Set("X-Agent-ID", "00000000-0000-0000-0000-000000000001")
			case "wrong scope":
				req.Header.Set(taskpolicy.HeaderRequirementID, "00000000-0000-0000-0000-000000000001")
			case "unapproved":
				dbfx.Exec(t, `UPDATE requirement SET issue_id = NULL WHERE id = $1`, f.Detail.ID)
			case "cancelled":
				dbfx.Exec(t, `UPDATE requirement SET cancelled_at = now() WHERE id = $1`, f.Detail.ID)
			}
			if mutation != "" {
				testutil.Call(t, testHandler.GetRequirementByID, req).Want(http.StatusForbidden)
				return
			}
			var got service.RequirementDetail
			testutil.Call(t, testHandler.GetRequirementByID, req).Want(http.StatusOK).JSON(&got)
			if got.IssueID == nil || *got.IssueID != uuidToString(f.Issue.ID) || len(got.Participants) == 0 || !got.Permissions.CanRead || got.Permissions.CanSubmitCandidate || got.Permissions.CanApprove || got.Permissions.CanAccept {
				t.Fatalf("execute task context has wrong binding or permissions: %+v", got)
			}
		})
	}
}

func TestCandidateFeatureOffAllowsReplayButNotNewWrite(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement candidate feature agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("93007").body(agentID), "candidate-feature-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "candidate-feature-open")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
	input := validCandidateRequest(t, opened.Requirement, 2)
	submitted := submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-feature-submit")
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	replayed := submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-feature-submit")
	if !requirementSameJSON(t, string(requirementJSON(submitted)), string(requirementJSON(replayed))) {
		t.Fatalf("feature-off replay changed response: %+v", replayed)
	}
	response := testutil.Call(t, testHandler.SubmitRequirementCandidate,
		candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, input, "candidate-feature-new")).Want(http.StatusForbidden)
	if response.Map()["code"] != "requirement_flow_disabled" {
		t.Fatalf("feature-off response = %s", response.Body)
	}
}
