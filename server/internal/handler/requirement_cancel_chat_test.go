package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCancelRequirementWithoutIssueCancelsPlanTasksAndPublishesAfterCommit(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement cancel plan agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("97311").body(agentID), "cancel-plan-create-97311")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "cancel-plan-open-97311")
	runningID := opened.PendingTask.TaskID
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, runningID)
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID)
	queuedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "chat_session_id": opened.Session.ID, "requirement_id": detail.ID, "mode": "plan", "status": "queued",
	})
	deferredID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "chat_session_id": opened.Session.ID, "requirement_id": detail.ID, "mode": "plan", "status": "deferred",
		"fire_at": testutil.Raw("now() + interval '1 hour'"),
	})

	bus := events.New()
	previousHandlerBus, previousTaskBus := testHandler.Bus, testHandler.TaskService.Bus
	testHandler.Bus, testHandler.TaskService.Bus = bus, bus
	t.Cleanup(func() { testHandler.Bus, testHandler.TaskService.Bus = previousHandlerBus, previousTaskBus })
	seen := make(map[string]int)
	bus.Subscribe(protocol.EventTaskCancelled, func(event events.Event) {
		for _, id := range []string{runningID, queuedID, deferredID} {
			if event.TaskID != id {
				continue
			}
			seen[id]++
			task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(id))
			if err != nil || task.Status != "cancelled" {
				t.Errorf("cancel event for %s was not post-commit: task=%+v err=%v", id, task, err)
			}
		}
	})

	var cancelled service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", detail.ID,
		service.CancelRequirementRequest{ExpectedRevision: ptr(opened.Requirement.Revision)}, "cancel-plan-97311")).Want(http.StatusOK).JSON(&cancelled)
	for _, id := range []string{runningID, queuedID, deferredID} {
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, id).Scan(&status)
		if status != "cancelled" || seen[id] != 1 {
			t.Fatalf("task %s status=%q cancel events=%d", id, status, seen[id])
		}
	}
	beforeMessages := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE task_id = $1 AND role = 'assistant'`, runningID)
	_, completed, err := testHandler.TaskService.CompleteTaskWithTransition(context.Background(), parseUUID(runningID), []byte(`{"output":"late completion"}`), "", "", "", false, "", "")
	if err != nil || completed {
		t.Fatalf("cancelled running Plan task completion = completed %v, err %v", completed, err)
	}
	afterMessages := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE task_id = $1 AND role = 'assistant'`, runningID)
	if afterMessages != beforeMessages {
		t.Fatalf("cancelled running Plan task wrote a completion message: before=%d after=%d", beforeMessages, afterMessages)
	}
}

func TestRequirementChatHardDeleteRequiresCancelAndOrdinaryDeleteUnchanged(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement delete chat agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("97312").body(agentID), "delete-requirement-chat-create-97312")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "delete-requirement-chat-open-97312")
	response := testutil.Call(t, testHandler.DeleteChatSession, chatRequestForTest(
		testUserID, http.MethodDelete, "/api/chat/sessions/"+opened.Session.ID, nil, opened.Session.ID)).Want(http.StatusConflict)
	if response.Map()["code"] != "requirement_chat_cancel_required" {
		t.Fatalf("requirement chat delete response = %s", response.Body)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, opened.Session.ID); got != 1 {
		t.Fatal("rejected requirement chat delete removed the session")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE id = $1 AND chat_session_id = $2`, detail.ID, opened.Session.ID); got != 1 {
		t.Fatal("rejected requirement chat delete changed the binding")
	}

	ordinary := dbfx.ChatSession(t, agentID)
	testutil.Call(t, testHandler.DeleteChatSession, chatRequestForTest(
		testUserID, http.MethodDelete, "/api/chat/sessions/"+ordinary, nil, ordinary)).Want(http.StatusNoContent)
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, ordinary); got != 0 {
		t.Fatal("ordinary chat delete behavior changed")
	}
}
