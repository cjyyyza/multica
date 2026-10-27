package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRequirementCreateReplayReturnsOriginalAfterStateAdvance(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement create replay agent")
	source := newRequirementSourceFixture("91013")
	body := source.body(agentID)
	first := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "create-replay-key",
		"id", testWorkspaceID)).Want(http.StatusCreated)
	var detail service.RequirementDetail
	first.JSON(&detail)
	registerRequirementCleanup(t, detail.ID)

	second := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "create-replay-key",
		"id", testWorkspaceID)).Want(http.StatusCreated)
	if !requirementSameJSON(t, first.Body.String(), second.Body.String()) {
		t.Fatalf("same-key response changed:\nfirst=%s\nsecond=%s", first.Body, second.Body)
	}
	viewer := dbfx.User(t, "Requirement create replay viewer", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	updateRequirementParticipantsForTest(t, testUserID, detail.ID, requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: viewer, Roles: []string{"viewer"}}), "create-replay-advance")
	if got := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID).Revision; got != 2 {
		t.Fatalf("advanced revision = %d", got)
	}
	replayed := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "create-replay-key",
		"id", testWorkspaceID)).Want(http.StatusCreated)
	if !requirementSameJSON(t, first.Body.String(), replayed.Body.String()) {
		t.Fatalf("state advance changed the original response:\noriginal=%s\nreplay=%s", first.Body, replayed.Body)
	}
	if got := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID).Revision; got != 2 {
		t.Fatalf("replay changed current revision to %d", got)
	}

	differentSource := newRequirementSourceFixture("91014")
	conflict := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", differentSource.body(agentID), "create-replay-key",
		"id", testWorkspaceID)).Want(http.StatusConflict)
	if conflict.Map()["code"] != "requirement_action_conflict" {
		t.Fatalf("different hash response = %s", conflict.Body)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3`, testWorkspaceID, differentSource.Source.Host, differentSource.Source.ID); got != 0 {
		t.Fatalf("different hash created %d requirements", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND action = 'create'`, detail.ID); got != 1 {
		t.Fatalf("create actions = %d, want 1", got)
	}
}

func TestRequirementParticipantsReplayCASAndRevokedRead(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement participant replay agent")
	source := newRequirementSourceFixture("91015")
	detail := createRequirementForTest(t, testUserID, source.body(agentID), "participants-replay-create")
	other := dbfx.User(t, "Requirement participant replay other", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, other, "member")

	firstInput := requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: other, Roles: []string{"approver"}})
	first := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", firstInput, "participants-replay-first",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusOK)
	secondInput := requirementParticipantsBody(2,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: other, Roles: []string{"viewer"}})
	updateRequirementParticipantsForTest(t, testUserID, detail.ID, secondInput, "participants-replay-second")
	if got := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID).Revision; got != 3 {
		t.Fatalf("revision before replay = %d, want 3", got)
	}

	replay := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", firstInput, "participants-replay-first",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusOK)
	if !requirementSameJSON(t, first.Body.String(), replay.Body.String()) {
		t.Fatalf("participant replay did not return original response:\noriginal=%s\nreplay=%s", first.Body, replay.Body)
	}
	current := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID)
	if current.Revision != 3 || len(current.Participants) != 2 || len(requirementRolesForTest(current, other)) != 1 || requirementRolesForTest(current, other)[0] != "viewer" {
		t.Fatalf("replay rewrote current state: %+v", current)
	}

	differentInput := requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: other, Roles: []string{"viewer"}})
	conflict := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", differentInput, "participants-replay-first",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusConflict)
	if conflict.Map()["code"] != "requirement_action_conflict" {
		t.Fatalf("different participant hash = %s", conflict.Body)
	}
	stale := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", differentInput, "participants-replay-stale",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusConflict)
	if stale.Map()["code"] != "revision_conflict" || stale.Map()["actual_revision"] != float64(3) {
		t.Fatalf("stale CAS response = %s", stale.Body)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND response_status IS NOT NULL`, detail.ID); got != 3 {
		t.Fatalf("completed actions = %d, want 3", got)
	}

	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id = $1 AND user_id = $2 AND role = 'requester'`, detail.ID, testUserID)
	dbfx.Insert(t, "requirement_participant", testutil.Cols{
		"workspace_id": testWorkspaceID, "requirement_id": detail.ID, "user_id": testUserID, "role": "viewer",
	})
	readOnlyReplay := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", firstInput, "participants-replay-first",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusOK)
	if !requirementSameJSON(t, first.Body.String(), readOnlyReplay.Body.String()) {
		t.Fatalf("read-only replay changed original response: %s", readOnlyReplay.Body)
	}

	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id = $1 AND user_id = $2`, detail.ID, testUserID)
	testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", source.body(agentID), "participants-replay-create",
		"id", testWorkspaceID)).Want(http.StatusForbidden)
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND response_status IS NOT NULL`, detail.ID); got != 3 {
		t.Fatalf("completed actions after revocation = %d, want 3", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_event WHERE requirement_id = $1`, detail.ID); got != 3 {
		t.Fatalf("events after rejected writes = %d, want 3", got)
	}
}
