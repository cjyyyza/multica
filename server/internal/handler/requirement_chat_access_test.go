package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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

func TestRequirementChatContentReadersIncludeParticipants(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudePublicAgent(t, "Requirement content reader agent")
	approver := dbfx.User(t, "Requirement content approver", uuid.NewString()+"@example.test")
	viewer := dbfx.User(t, "Requirement content viewer", uuid.NewString()+"@example.test")
	outsider := dbfx.User(t, "Requirement content outsider", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, approver, "member")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	dbfx.Member(t, testWorkspaceID, outsider, "member")
	body := newRequirementSourceFixture("94011").body(agentID)
	body["participants"] = []service.RequirementParticipantInput{
		{UserID: approver, Roles: []string{"approver"}},
		{UserID: viewer, Roles: []string{"viewer"}},
	}
	detail := createRequirementForTest(t, testUserID, body, "content-reader-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "content-reader-open")
	if opened.PendingTask == nil || opened.PendingTask.TaskID == "" {
		t.Fatal("requirement open did not create a chat task")
	}
	taskID := opened.PendingTask.TaskID
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now(), result = '{"output":"requirement chat reply"}'::jsonb, error = 'requirement chat error' WHERE id = $1`, taskID)
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": taskID,
		"seq":     1,
		"type":    "text",
		"content": "requirement chat transcript",
	})

	readTranscript := func(userID string) *testutil.Response {
		req := withURLParam(newRequestAs(userID, http.MethodGet, "/api/tasks/"+taskID+"/messages", nil), "taskId", taskID)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
		return testutil.Call(t, testHandler.ListTaskMessagesByUser, req)
	}
	for _, userID := range []string{testUserID, approver, viewer} {
		var history []protocol.TaskMessagePayload
		readTranscript(userID).Want(http.StatusOK).JSON(&history)
		if len(history) != 1 || history[0].Content != "requirement chat transcript" {
			t.Fatalf("reader %s transcript = %+v", userID, history)
		}
	}
	readTranscript(outsider).Want(http.StatusNotFound)

	listTasks := func(userID string) map[string]AgentTaskResponse {
		req := withURLParam(newRequestAs(userID, http.MethodGet, "/api/agents/"+agentID+"/tasks", nil), "id", agentID)
		var tasks []AgentTaskResponse
		testutil.Call(t, testHandler.ListAgentTasks, req).Want(http.StatusOK).JSON(&tasks)
		byID := map[string]AgentTaskResponse{}
		for _, task := range tasks {
			byID[task.ID] = task
		}
		return byID
	}
	for _, userID := range []string{testUserID, approver, viewer} {
		task, ok := listTasks(userID)[taskID]
		if !ok || task.Result == nil || task.Error == nil || *task.Error != "requirement chat error" {
			t.Fatalf("reader %s task = %+v, want result and error", userID, task)
		}
	}
	foreign, ok := listTasks(outsider)[taskID]
	if !ok {
		t.Fatal("outsider lost the task row")
	}
	if foreign.Result != nil || foreign.Error != nil {
		t.Fatalf("outsider sees result=%v error=%v, want both redacted", foreign.Result, foreign.Error)
	}

	ordinary := dbfx.ChatSession(t, agentID, testutil.Cols{"creator_id": testUserID})
	ordinaryTask := dbfx.Task(t, agentID, testutil.Cols{
		"chat_session_id": ordinary,
		"status":          "completed",
		"completed_at":    testutil.Raw("now()"),
		"result":          testutil.Raw(`'{"output":"ordinary reply"}'::jsonb`),
		"error":           "ordinary error",
	})
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": ordinaryTask,
		"seq":     1,
		"type":    "text",
		"content": "ordinary transcript",
	})
	readOrdinary := func(userID string) *testutil.Response {
		req := withURLParam(newRequestAs(userID, http.MethodGet, "/api/tasks/"+ordinaryTask+"/messages", nil), "taskId", ordinaryTask)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
		return testutil.Call(t, testHandler.ListTaskMessagesByUser, req)
	}
	var ownHistory []protocol.TaskMessagePayload
	readOrdinary(testUserID).Want(http.StatusOK).JSON(&ownHistory)
	if len(ownHistory) != 1 || ownHistory[0].Content != "ordinary transcript" {
		t.Fatalf("creator ordinary transcript = %+v", ownHistory)
	}
	readOrdinary(approver).Want(http.StatusNotFound)
	ordinaryRow, ok := listTasks(approver)[ordinaryTask]
	if !ok {
		t.Fatal("ordinary chat task row missing from the non-creator list")
	}
	if ordinaryRow.Result != nil || ordinaryRow.Error != nil {
		t.Fatalf("non-creator ordinary task result=%v error=%v, want both redacted", ordinaryRow.Result, ordinaryRow.Error)
	}

	testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(
		viewer, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/messages",
		map[string]any{"content": "Viewer must not write"}, opened.Session.ID)).Want(http.StatusForbidden)

	sharedBus := testHandler.Bus
	testHandler.Bus = events.New()
	t.Cleanup(func() { testHandler.Bus = sharedBus })
	var mu sync.Mutex
	var chatEvent events.Event
	var taskEvent events.Event
	var pinEvent events.Event
	var readEvent events.Event
	testHandler.Bus.Subscribe(protocol.EventChatMessage, func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.ChatSessionID == opened.Session.ID {
			chatEvent = e
		}
	})
	testHandler.Bus.Subscribe(protocol.EventTaskMessage, func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.TaskID == taskID {
			taskEvent = e
		}
	})
	testHandler.Bus.Subscribe(protocol.EventChatSessionUpdated, func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.ChatSessionID == opened.Session.ID {
			pinEvent = e
		}
	})
	testHandler.Bus.Subscribe(protocol.EventChatSessionRead, func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.ChatSessionID == opened.Session.ID {
			readEvent = e
		}
	})
	testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(
		approver, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/messages",
		map[string]any{"content": "participant turn"}, opened.Session.ID)).Want(http.StatusCreated)
	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, []any{
		map[string]any{"seq": 2, "type": "text", "content": "streamed requirement text"},
	})).Want(http.StatusOK)
	testutil.Call(t, testHandler.SetChatSessionPinned, chatRequestForTest(
		testUserID, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/pin",
		map[string]any{"pinned": true}, opened.Session.ID)).Want(http.StatusOK)
	testutil.Call(t, testHandler.MarkChatSessionRead, chatRequestForTest(
		testUserID, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/read", nil, opened.Session.ID)).Want(http.StatusNoContent)

	mu.Lock()
	defer mu.Unlock()
	if chatEvent.ChatSessionOwnerID != testUserID {
		t.Fatalf("chat owner = %q, want creator %q", chatEvent.ChatSessionOwnerID, testUserID)
	}
	wantReaders := map[string]bool{testUserID: true, approver: true, viewer: true}
	assertChatReaders(t, "chat:message", chatEvent.ChatContentReaderIDs, wantReaders, outsider)
	assertChatReaders(t, "task:message", taskEvent.ChatContentReaderIDs, wantReaders, outsider)
	if taskEvent.ChatSessionOwnerID != testUserID {
		t.Fatalf("task:message owner = %q, want creator %q", taskEvent.ChatSessionOwnerID, testUserID)
	}
	creatorOnly := map[string]bool{testUserID: true}
	assertChatReaders(t, "chat:session_updated pin", pinEvent.ChatContentReaderIDs, creatorOnly, approver)
	assertChatReaders(t, "chat:session_read", readEvent.ChatContentReaderIDs, creatorOnly, approver)
}

func assertChatReaders(t *testing.T, label string, got []string, want map[string]bool, outsider string) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range got {
		if id == outsider {
			t.Fatalf("%s included outsider %s in %v", label, outsider, got)
		}
		seen[id] = true
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("%s readers = %v, missing %s", label, got, id)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("%s readers = %v, want %v", label, got, want)
	}
}
