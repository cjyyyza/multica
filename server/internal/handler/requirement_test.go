package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func enableRequirementFlowForTest(t *testing.T) {
	t.Helper()
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, true)
}

func TestCreateRequirementRejectsUnsupportedPlanRuntimeWithoutReservingSource(t *testing.T) {
	enableRequirementFlowForTest(t)
	for i, tc := range []struct {
		name, provider   string
		profile, unbound bool
	}{
		{name: "unsupported provider", provider: "codex"},
		{name: "custom profile", provider: "claude", profile: true},
		{name: "unbound", provider: "claude", unbound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeID := dbfx.Runtime(t, "Requirement rejected runtime "+tc.name, testutil.Cols{"provider": tc.provider})
			agentID := dbfx.Agent(t, "Requirement rejected agent "+tc.name, runtimeID)
			if tc.profile {
				dbfx.Exec(t, `UPDATE agent_runtime SET profile_id = $2 WHERE id = $1`, runtimeID, uuid.NewString())
			}
			if tc.unbound {
				dbfx.Exec(t, `UPDATE agent SET runtime_id = NULL WHERE id = $1`, agentID)
			}
			source := newRequirementSourceFixture(fmt.Sprintf("998%03d", i))
			t.Cleanup(func() {
				if row, err := testHandler.Queries.GetRequirementBySource(context.Background(), db.GetRequirementBySourceParams{
					WorkspaceID: parseUUID(testWorkspaceID), SourceHost: source.Source.Host, ExternalID: source.Source.ID,
				}); err == nil {
					registerRequirementCleanup(t, uuidToString(row.ID))
				}
			})
			key := "create-unsupported-" + tc.name
			response := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
				testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements",
				source.body(agentID), key, "id", testWorkspaceID)).Want(http.StatusConflict)
			if response.Map()["code"] != "plan_mode_not_supported" {
				t.Fatalf("unsupported runtime response = %s", response.Body)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE workspace_id = $1 AND external_id = $2`, testWorkspaceID, source.Source.ID); got != 0 {
				t.Fatalf("unsupported runtime reserved source: %d requirements", got)
			}
			_, supportedAgent := newRequirementClaudeAgent(t, "Requirement retry supported "+tc.name)
			createRequirementForTest(t, testUserID, source.body(supportedAgent), key)
		})
	}
}

type requirementSourceFixture struct {
	Source      yixiezuo.Source
	Snapshot    yixiezuo.Snapshot
	Raw         json.RawMessage
	Digest      string
	LockVersion *int64
}

func newRequirementSourceFixture(externalID string) requirementSourceFixture {
	source := yixiezuo.Source{
		Host: "dj01.pm.netease.com", ID: externalID,
		URL: "https://dj01.pm.netease.com/issues/" + externalID,
	}
	lockVersion := int64(3)
	snapshot := yixiezuo.Snapshot{
		Source: source, Title: "Requirement " + externalID, Description: "Source material",
		Status: "open", Priority: "normal", LockVersion: &lockVersion,
	}
	snapshot.Digest = snapshot.Fingerprint()
	return requirementSourceFixture{
		Source: source, Snapshot: snapshot, Raw: requirementJSON(snapshot),
		Digest: snapshot.Digest, LockVersion: &lockVersion,
	}
}

func (f requirementSourceFixture) body(agentID string) map[string]any {
	return map[string]any{
		"sourceHost": "yixiezuo", "externalId": f.Source.ID, "sourceUrl": f.Source.URL,
		"sourceSnapshot": f.Raw, "sourceDigest": f.Digest, "sourceLockVersion": f.LockVersion,
		"agentId": agentID, "projectId": nil,
	}
}

func requirementJSON(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func requirementSameJSON(t *testing.T, want, got string) bool {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	wantCanonical := requirementJSON(wantValue)
	gotCanonical := requirementJSON(gotValue)
	return string(wantCanonical) == string(gotCanonical)
}

func requirementRequest(userID, method, path string, body any, idempotencyKey string, params ...string) *http.Request {
	req := testutil.JSONRequest(method, path, body)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	req = testutil.WithURLParams(req, params...)
	workspaceID := testWorkspaceID
	for i := 0; i+1 < len(params); i += 2 {
		if params[i] == "id" {
			workspaceID = params[i+1]
		}
	}
	if member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID: parseUUID(userID), WorkspaceID: parseUUID(workspaceID),
	}); err == nil {
		req = req.WithContext(middleware.SetMemberContext(req.Context(), workspaceID, member))
	}
	return req
}

func createRequirementForTest(t *testing.T, userID string, body map[string]any, key string) service.RequirementDetail {
	t.Helper()
	response := testutil.Call(t, testHandler.CreateRequirement,
		requirementRequest(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, key, "id", testWorkspaceID)).
		Want(http.StatusCreated)
	var detail service.RequirementDetail
	response.JSON(&detail)
	registerRequirementCleanup(t, detail.ID)
	return detail
}

func registerRequirementCleanup(t *testing.T, requirementID string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM requirement WHERE id = $1`, requirementID)
	for _, table := range []string{
		"popo_bridge_command", "requirement_outbox", "requirement_action", "requirement_event", "requirement_wakeup",
		"requirement_approval", "requirement_candidate", "requirement_participant",
	} {
		dbfx.Cleanup(t, `DELETE FROM `+table+` WHERE requirement_id = $1`, requirementID)
	}
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE requirement_id = $1`, requirementID)
}

func requirementDetailForTest(t *testing.T, userID, workspaceID, requirementID string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.GetRequirementByID, requirementRequest(
		userID, http.MethodGet, "/api/workspaces/"+workspaceID+"/requirements/"+requirementID, nil, "",
		"id", workspaceID, "requirementId", requirementID)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func requirementDetailByChatForTest(t *testing.T, userID, workspaceID, chatSessionID string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.GetRequirementByChat, requirementRequest(
		userID, http.MethodGet, "/api/workspaces/"+workspaceID+"/requirements?chat_session_id="+chatSessionID, nil, "",
		"id", workspaceID)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func requirementParticipantsBody(revision int64, participants ...service.RequirementParticipantInput) map[string]any {
	return map[string]any{"expectedRevision": revision, "participants": participants}
}

func requirementRolesForTest(detail service.RequirementDetail, userID string) []string {
	for _, participant := range detail.Participants {
		if participant.UserID == userID {
			return participant.Roles
		}
	}
	return nil
}

func updateRequirementParticipantsForTest(t *testing.T, userID, requirementID string, body map[string]any, key string) service.RequirementDetail {
	t.Helper()
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+requirementID+"/participants", body, key,
		"id", testWorkspaceID, "requirementId", requirementID)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func publicRequirementAgent(t *testing.T, name string) string {
	t.Helper()
	_, agentID := newRequirementClaudePublicAgent(t, name)
	return agentID
}

func TestRequirementCreateGetAndByChatContract(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement contract agent")
	projectID := dbfx.Project(t, "Requirement contract project")
	source := newRequirementSourceFixture("91001")
	body := source.body(agentID)
	body["projectId"] = projectID
	detail := createRequirementForTest(t, testUserID, body, "contract-create")
	if detail.ID == "" || detail.WorkspaceID != testWorkspaceID || detail.Revision != 1 {
		t.Fatalf("unexpected detail identity: %+v", detail)
	}
	if detail.State != "intake_pending" || detail.IntakeState != "intake_pending" || detail.SourceHost != source.Source.Host {
		t.Fatalf("unexpected intake/source projection: %+v", detail)
	}
	if detail.IntakeOperationID != nil || len(detail.Operations) != 0 || len(detail.Deliveries) != 1 || detail.Deliveries[0].State != "pending" {
		t.Fatalf("unexpected intake projections: %+v", detail)
	}
	if len(detail.Participants) != 1 || detail.Participants[0].UserID != testUserID ||
		fmt.Sprint(detail.Participants[0].Roles) != "[requester approver]" {
		t.Fatalf("default participants = %+v", detail.Participants)
	}
	if !detail.Permissions.CanRead || !detail.Permissions.CanOpen || !detail.Permissions.CanManageParticipants ||
		!detail.Permissions.CanCancel || detail.Permissions.CanApprove || detail.Permissions.CanSubmitCandidate {
		t.Fatalf("unexpected permissions: %+v", detail.Permissions)
	}
	if detail.LatestCandidate != nil || string(detail.Issue) != "null" || string(detail.Run) != "null" {
		t.Fatalf("unexpected optional projections: candidate=%+v issue=%s run=%s", detail.LatestCandidate, detail.Issue, detail.Run)
	}

	got := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID)
	if got.ID != detail.ID || got.Revision != 1 || got.ProjectID == nil || *got.ProjectID != projectID {
		t.Fatalf("get detail = %+v", got)
	}
	chatID := dbfx.ChatSession(t, agentID)
	dbfx.Exec(t, `UPDATE requirement SET chat_session_id = $1 WHERE id = $2`, chatID, detail.ID)
	byChat := requirementDetailByChatForTest(t, testUserID, testWorkspaceID, chatID)
	if byChat.ID != detail.ID || byChat.ChatSessionID == nil || *byChat.ChatSessionID != chatID {
		t.Fatalf("byChat detail = %+v", byChat)
	}

	testutil.Call(t, testHandler.GetRequirementByChat, requirementRequest(
		testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements?chat_session_id="+uuid.NewString(), nil, "",
		"id", testWorkspaceID)).Want(http.StatusNotFound)
	outsider := dbfx.User(t, "Requirement outsider", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, outsider, "member")
	testutil.Call(t, testHandler.GetRequirementByChat, requirementRequest(
		outsider, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements?chat_session_id="+chatID, nil, "",
		"id", testWorkspaceID)).Want(http.StatusForbidden)
	admin := dbfx.User(t, "Requirement admin nonparticipant", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, admin, "admin")
	testutil.Call(t, testHandler.GetRequirementByID, requirementRequest(
		admin, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID, nil, "",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusForbidden)

	otherWorkspace := dbfx.Workspace(t, "Requirement cross workspace", "requirement-cross-"+uuid.NewString())
	dbfx.Member(t, otherWorkspace, testUserID, "owner")
	testutil.Call(t, testHandler.GetRequirementByID, requirementRequest(
		testUserID, http.MethodGet, "/api/workspaces/"+otherWorkspace+"/requirements/"+detail.ID, nil, "",
		"id", otherWorkspace, "requirementId", detail.ID)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.GetRequirementByChat, requirementRequest(
		testUserID, http.MethodGet, "/api/workspaces/"+otherWorkspace+"/requirements?chat_session_id="+chatID, nil, "",
		"id", otherWorkspace)).Want(http.StatusNotFound)
}

func TestRequirementParticipantReadAndFullUpdate(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement participant agent")
	viewer := dbfx.User(t, "Requirement viewer", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	source := newRequirementSourceFixture("91002")
	body := source.body(agentID)
	body["participants"] = []service.RequirementParticipantInput{{UserID: viewer, Roles: []string{"viewer"}}}
	detail := createRequirementForTest(t, testUserID, body, "participants-create")

	viewerDetail := requirementDetailForTest(t, viewer, testWorkspaceID, detail.ID)
	if !viewerDetail.Permissions.CanRead || viewerDetail.Permissions.CanManageParticipants || viewerDetail.Permissions.CanApprove {
		t.Fatalf("viewer permissions = %+v", viewerDetail.Permissions)
	}
	testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		viewer, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants",
		requirementParticipantsBody(1,
			service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
			service.RequirementParticipantInput{UserID: viewer, Roles: []string{"approver"}}), "viewer-update",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusForbidden)

	updated := updateRequirementParticipantsForTest(t, testUserID, detail.ID, requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester"}},
		service.RequirementParticipantInput{UserID: viewer, Roles: []string{"approver"}}), "participants-update-1")
	if updated.Revision != 2 || len(updated.Participants) != 2 {
		t.Fatalf("updated detail = %+v", updated)
	}
	viewerDetail = requirementDetailForTest(t, viewer, testWorkspaceID, detail.ID)
	if !viewerDetail.Permissions.CanReconcile || !viewerDetail.Permissions.CanCancel || viewerDetail.Permissions.CanApprove {
		t.Fatalf("approver permissions = %+v", viewerDetail.Permissions)
	}

	for name, input := range map[string]map[string]any{
		"missing requester": requirementParticipantsBody(2,
			service.RequirementParticipantInput{UserID: viewer, Roles: []string{"approver"}}),
		"missing approver": requirementParticipantsBody(2,
			service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester"}}),
		"nonmember": requirementParticipantsBody(2,
			service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
			service.RequirementParticipantInput{UserID: uuid.NewString(), Roles: []string{"viewer"}}),
	} {
		t.Run(name, func(t *testing.T) {
			testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
				testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants", input, "invalid-"+name,
				"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusBadRequest)
		})
	}
	cas := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/participants",
		requirementParticipantsBody(1,
			service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
			service.RequirementParticipantInput{UserID: viewer, Roles: []string{"viewer"}}), "participants-stale",
		"id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusConflict)
	if got := cas.Map()["code"]; got != "revision_conflict" {
		t.Fatalf("CAS code = %v", got)
	}
	if got := requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID).Revision; got != 2 {
		t.Fatalf("revision after rejected updates = %d, want 2", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND response_status IS NOT NULL`, detail.ID); got != 2 {
		t.Fatalf("completed actions = %d, want 2", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_event WHERE requirement_id = $1`, detail.ID); got != 2 {
		t.Fatalf("events = %d, want 2", got)
	}
}

func TestRequirementCreateValidation(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement validation agent")
	t.Run("invalid snapshot", func(t *testing.T) {
		source := newRequirementSourceFixture("91003")
		source.Snapshot.Title = ""
		body := source.body(agentID)
		body["sourceSnapshot"] = requirementJSON(source.Snapshot)
		testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "invalid-snapshot",
			"id", testWorkspaceID)).Want(http.StatusBadRequest)
		if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE idempotency_key = 'invalid-snapshot'`); got != 0 {
			t.Fatalf("completed invalid action rows = %d", got)
		}
	})
	t.Run("private agent not invocable", func(t *testing.T) {
		other := dbfx.User(t, "Requirement private agent owner", uuid.NewString()+"@example.test")
		privateAgent := dbfx.Agent(t, "Requirement private agent", handlerTestRuntimeID(t), testutil.Cols{"owner_id": other})
		source := newRequirementSourceFixture("91004")
		testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", source.body(privateAgent), "private-agent-create",
			"id", testWorkspaceID)).Want(http.StatusForbidden)
	})
	t.Run("foreign project", func(t *testing.T) {
		otherWorkspace := dbfx.Workspace(t, "Requirement foreign project workspace", "requirement-project-"+uuid.NewString())
		foreignProject := dbfx.Project(t, "Requirement foreign project", testutil.Cols{"workspace_id": otherWorkspace})
		source := newRequirementSourceFixture("91005")
		body := source.body(agentID)
		body["projectId"] = foreignProject
		testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "foreign-project-create",
			"id", testWorkspaceID)).Want(http.StatusNotFound)
	})
	t.Run("participant not member", func(t *testing.T) {
		source := newRequirementSourceFixture("91006")
		body := source.body(agentID)
		body["participants"] = []service.RequirementParticipantInput{{UserID: uuid.NewString(), Roles: []string{"viewer"}}}
		testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", body, "nonmember-participant-create",
			"id", testWorkspaceID)).Want(http.StatusBadRequest)
	})
	t.Run("missing idempotency key", func(t *testing.T) {
		source := newRequirementSourceFixture("91007")
		testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", source.body(agentID), "",
			"id", testWorkspaceID)).Want(http.StatusBadRequest)
		if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE source_host = $1 AND external_id = $2`, source.Source.Host, source.Source.ID); got != 0 {
			t.Fatalf("requirements created without key = %d", got)
		}
	})
}

func TestRequirementIntakeOperationValidationAndProjection(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement intake agent")
	newOperation := func(source requirementSourceFixture, requestedBy, state string) string {
		return dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
			"workspace_id": testWorkspaceID, "requested_by": requestedBy, "kind": "preview",
			"payload": requirementJSON(yixiezuo.OperationPayload{Source: source.Source}), "result": source.Raw, "state": state,
		})
	}
	source := newRequirementSourceFixture("91008")
	operationID := newOperation(source, testUserID, "succeeded")
	body := source.body(agentID)
	body["intakeOperationId"] = operationID
	detail := createRequirementForTest(t, testUserID, body, "intake-create")
	if detail.IntakeOperationID == nil || *detail.IntakeOperationID != operationID || len(detail.Operations) != 1 {
		t.Fatalf("intake detail = %+v", detail)
	}
	if detail.Operations[0].ID != operationID || detail.Operations[0].Kind != "preview" || detail.Operations[0].State != "succeeded" {
		t.Fatalf("intake operation = %+v", detail.Operations[0])
	}

	other := dbfx.User(t, "Requirement other intake user", uuid.NewString()+"@example.test")
	unreadableSource := newRequirementSourceFixture("91009")
	unreadableOperation := newOperation(unreadableSource, other, "succeeded")
	unreadableBody := unreadableSource.body(agentID)
	unreadableBody["intakeOperationId"] = unreadableOperation
	testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", unreadableBody, "unreadable-intake",
		"id", testWorkspaceID)).Want(http.StatusForbidden)

	mismatchSource := newRequirementSourceFixture("91010")
	mismatchOperation := newOperation(newRequirementSourceFixture("91999"), testUserID, "succeeded")
	mismatchBody := mismatchSource.body(agentID)
	mismatchBody["intakeOperationId"] = mismatchOperation
	testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", mismatchBody, "mismatch-intake",
		"id", testWorkspaceID)).Want(http.StatusConflict)

	pendingSource := newRequirementSourceFixture("91011")
	pendingOperation := newOperation(pendingSource, testUserID, "pending")
	pendingBody := pendingSource.body(agentID)
	pendingBody["intakeOperationId"] = pendingOperation
	testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", pendingBody, "pending-intake",
		"id", testWorkspaceID)).Want(http.StatusConflict)
}

func TestRequirementSourceConflictHidesExistingID(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID := publicRequirementAgent(t, "Requirement source conflict agent")
	source := newRequirementSourceFixture("91012")
	detail := createRequirementForTest(t, testUserID, source.body(agentID), "source-conflict-original")
	outsider := dbfx.User(t, "Requirement source conflict outsider", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, outsider, "member")

	hidden := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		outsider, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", source.body(agentID), "source-conflict-hidden",
		"id", testWorkspaceID)).Want(http.StatusConflict)
	hiddenBody := hidden.Map()
	if hiddenBody["code"] != "requirement_source_already_exists" {
		t.Fatalf("source conflict = %+v", hiddenBody)
	}
	if _, exists := hiddenBody["existing_requirement_id"]; exists {
		t.Fatalf("source conflict leaked existing id: %+v", hiddenBody)
	}

	updateRequirementParticipantsForTest(t, testUserID, detail.ID, requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: outsider, Roles: []string{"viewer"}}), "source-conflict-share")
	visible := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		outsider, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", source.body(agentID), "source-conflict-visible",
		"id", testWorkspaceID)).Want(http.StatusConflict)
	if got := visible.Map()["existing_requirement_id"]; got != detail.ID {
		t.Fatalf("visible existing requirement id = %v, want %s", got, detail.ID)
	}
}
