package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func chatRequestForTest(userID, method, path string, body any, sessionID string) *http.Request {
	return requirementRequest(userID, method, path, body, "", "sessionId", sessionID)
}

func TestRequirementChatProjectIsImmutableAndApproveKeepsIt(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	projectID := dbfx.Project(t, "Requirement original project")
	replacementProjectID := dbfx.Project(t, "Requirement replacement project")
	fixture := newRequirementApproveFixture(t, "94002", projectID)
	opened := fixture.Opened

	for _, project := range []any{replacementProjectID, nil} {
		testutil.Call(t, testHandler.UpdateChatSession, chatRequestForTest(
			testUserID, http.MethodPatch, "/api/chat/sessions/"+opened.Session.ID,
			map[string]any{"project_id": project}, opened.Session.ID)).Want(http.StatusForbidden)
		var stored string
		dbfx.QueryRow(t, `SELECT project_id::text FROM chat_session WHERE id = $1`, opened.Session.ID).Scan(&stored)
		if stored != projectID {
			t.Fatalf("requirement chat project changed to %q, want %q", stored, projectID)
		}
	}

	ordinary := dbfx.ChatSession(t, fixture.AgentID, testutil.Cols{"project_id": projectID, "explicitly_created_at": testutil.Raw("now()")})
	testutil.Call(t, testHandler.UpdateChatSession, chatRequestForTest(
		testUserID, http.MethodPatch, "/api/chat/sessions/"+ordinary,
		map[string]any{"project_id": replacementProjectID}, ordinary)).Want(http.StatusOK)

	approved, _ := approveRequirementForTest(t, fixture, "project-chat-approve")
	issue := approveIssueForTest(t, approved)
	if issue.ProjectID == nil || *issue.ProjectID != projectID {
		t.Fatalf("approved issue project = %+v, want %q", issue.ProjectID, projectID)
	}
	var stored string
	dbfx.QueryRow(t, `SELECT project_id::text FROM chat_session WHERE id = $1`, opened.Session.ID).Scan(&stored)
	if stored != projectID {
		t.Fatalf("candidate/approve changed requirement chat project to %q", stored)
	}
}

func TestRequirementChatParticipantAccessAndOrdinaryChatRegression(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudePublicAgent(t, "Requirement participant chat agent")
	approver := dbfx.User(t, "Requirement chat approver", uuid.NewString()+"@example.test")
	viewer := dbfx.User(t, "Requirement chat viewer", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, approver, "member")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	source := newRequirementSourceFixture("94001")
	body := source.body(agentID)
	body["participants"] = []service.RequirementParticipantInput{
		{UserID: approver, Roles: []string{"approver"}},
		{UserID: viewer, Roles: []string{"viewer"}},
	}
	detail := createRequirementForTest(t, testUserID, body, "participant-chat-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "participant-chat-open")

	var session ChatSessionResponse
	testutil.Call(t, testHandler.GetChatSession, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+opened.Session.ID, nil, opened.Session.ID)).Want(http.StatusOK).JSON(&session)
	if session.ID != opened.Session.ID || session.Pinned || session.HasUnread || session.UnreadCount != 0 {
		t.Fatalf("participant session projection = %+v", session)
	}
	var messages []ChatMessageResponse
	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+opened.Session.ID+"/messages", nil, opened.Session.ID)).Want(http.StatusOK).JSON(&messages)
	if len(messages) != 1 || messages[0].MessageKind != "requirement_opening" {
		t.Fatalf("participant messages = %+v", messages)
	}
	for _, message := range messages {
		if message.MessageKind == "requirement_context" {
			t.Fatal("hidden source context leaked to participant")
		}
	}

	var sessions []ChatSessionResponse
	testutil.Call(t, testHandler.ListChatSessions, requirementRequest(
		approver, http.MethodGet, "/api/chat/sessions", nil, "")).Want(http.StatusOK).JSON(&sessions)
	found := false
	for _, item := range sessions {
		if item.ID == opened.Session.ID {
			found = true
			if item.Pinned || item.HasUnread || item.UnreadCount != 0 {
				t.Fatalf("participant list preferences leaked: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("participant session missing from list: %+v", sessions)
	}
	var pending PendingChatTasksResponse
	testutil.Call(t, testHandler.ListPendingChatTasks, requirementRequest(
		approver, http.MethodGet, "/api/chat/pending", nil, "")).Want(http.StatusOK).JSON(&pending)
	if len(pending.Tasks) == 0 {
		t.Fatal("participant pending tasks missing")
	}
	var hasPending HasPendingChatTasksResponse
	testutil.Call(t, testHandler.HasPendingChatTasks, requirementRequest(
		approver, http.MethodGet, "/api/chat/pending/exists", nil, "")).Want(http.StatusOK).JSON(&hasPending)
	if !hasPending.HasPending {
		t.Fatal("participant pending boolean is false")
	}
	var pendingTask PendingChatTaskResponse
	testutil.Call(t, testHandler.GetPendingChatTask, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+opened.Session.ID+"/pending", nil, opened.Session.ID)).Want(http.StatusOK).JSON(&pendingTask)
	if pendingTask.TaskID != opened.PendingTask.TaskID {
		t.Fatalf("participant pending task = %+v", pendingTask)
	}

	send := testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(
		approver, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/messages",
		map[string]any{"content": "Please clarify the constraints"}, opened.Session.ID)).Want(http.StatusCreated)
	var sendBody map[string]any
	send.JSON(&sendBody)
	clarificationTaskID, _ := sendBody["task_id"].(string)
	if clarificationTaskID == "" {
		t.Fatalf("clarification response = %s", send.Body)
	}
	var mode, requirementID string
	if err := testPool.QueryRow(context.Background(), `SELECT mode, requirement_id::text FROM agent_task_queue WHERE id = $1`, clarificationTaskID).Scan(&mode, &requirementID); err != nil {
		t.Fatal(err)
	}
	if mode != "plan" || requirementID != detail.ID {
		t.Fatalf("clarification task mode/requirement = %s %s", mode, requirementID)
	}

	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(
		viewer, http.MethodGet, "/api/chat/sessions/"+opened.Session.ID+"/messages", nil, opened.Session.ID)).Want(http.StatusOK)
	testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(
		viewer, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/messages",
		map[string]any{"content": "Viewer must not write"}, opened.Session.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.CancelTaskByUser, requirementRequest(
		viewer, http.MethodPost, "/api/tasks/"+opened.PendingTask.TaskID+"/cancel", nil, "", "taskId", opened.PendingTask.TaskID)).Want(http.StatusForbidden)

	ordinary := dbfx.ChatSession(t, agentID)
	testutil.Call(t, testHandler.GetChatSession, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+ordinary, nil, ordinary)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+ordinary+"/messages", nil, ordinary)).Want(http.StatusForbidden)

	var beforePinned, beforeRead string
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(pinned_at::text, ''), COALESCE(last_read_at::text, '') FROM chat_session WHERE id = $1`, opened.Session.ID).Scan(&beforePinned, &beforeRead); err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.SetChatSessionPinned, chatRequestForTest(
		approver, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/pin", map[string]any{"pinned": true}, opened.Session.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.MarkChatSessionRead, chatRequestForTest(
		approver, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/read", nil, opened.Session.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ListChatDraftRestores, chatRequestForTest(
		approver, http.MethodGet, "/api/chat/sessions/"+opened.Session.ID+"/draft-restores", nil, opened.Session.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.UpdateChatSession, chatRequestForTest(
		approver, http.MethodPatch, "/api/chat/sessions/"+opened.Session.ID, map[string]any{"title": "Participant must not rename"}, opened.Session.ID)).Want(http.StatusForbidden)
	var afterPinned, afterRead string
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(pinned_at::text, ''), COALESCE(last_read_at::text, '') FROM chat_session WHERE id = $1`, opened.Session.ID).Scan(&afterPinned, &afterRead); err != nil {
		t.Fatal(err)
	}
	if afterPinned != beforePinned || afterRead != beforeRead {
		t.Fatalf("participant changed creator preferences: pinned=%s->%s read=%s->%s", beforePinned, afterPinned, beforeRead, afterRead)
	}
}
