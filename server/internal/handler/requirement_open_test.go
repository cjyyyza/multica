package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newRequirementClaudeAgent(t *testing.T, name string) (string, string) {
	t.Helper()
	runtimeID := dbfx.Runtime(t, name+" runtime", testutil.Cols{"provider": "claude"})
	agentID := dbfx.Agent(t, name, runtimeID)
	return runtimeID, agentID
}

func newRequirementClaudePublicAgent(t *testing.T, name string) (string, string) {
	t.Helper()
	runtimeID, agentID := newRequirementClaudeAgent(t, name)
	dbfx.Exec(t, `UPDATE agent SET permission_mode = 'public_to', visibility = 'workspace' WHERE id = $1`, agentID)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{
		"agent_id": agentID, "target_type": "workspace", "target_id": testWorkspaceID, "created_by": testUserID,
	})
	return runtimeID, agentID
}

func buildRequirementOpenForTest(detail service.RequirementDetail, session db.ChatSession, task *db.AgentTaskQueue) (json.RawMessage, error) {
	var pending *PendingChatTaskResponse
	if task != nil {
		pending = &PendingChatTaskResponse{
			TaskID: uuidToString(task.ID), Status: task.Status,
			CreatedAt: timestampToString(task.CreatedAt), SupportsQueue: true,
		}
	}
	return json.Marshal(requirementOpenResponse{Requirement: detail, Session: chatSessionToResponse(session), PendingTask: pending})
}

func openRequirementForTest(t *testing.T, userID, requirementID string, revision int64, key string) requirementOpenResponse {
	t.Helper()
	var response requirementOpenResponse
	testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
		userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+requirementID+"/open",
		map[string]any{"expectedRevision": revision}, key,
		"id", testWorkspaceID, "requirementId", requirementID)).Want(http.StatusOK).JSON(&response)
	registerRequirementChatCleanup(t, response.Session.ID)
	return response
}

func registerRequirementChatCleanup(t *testing.T, chatSessionID string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM chat_session WHERE id = $1`, chatSessionID)
	dbfx.Cleanup(t, `DELETE FROM chat_message WHERE chat_session_id = $1`, chatSessionID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, chatSessionID)
}

func requirementServiceForTest() *service.RequirementService {
	svc := service.NewRequirementService(testHandler.Queries, testHandler.TxStarter)
	svc.Tasks = testHandler.TaskService
	svc.Issues = testHandler.IssueService
	svc.FeatureFlags = testHandler.FeatureFlags
	svc.RunProjector = func(workspaceID string, task db.AgentTaskQueue) (json.RawMessage, error) {
		return json.Marshal(taskToResponse(task, workspaceID))
	}
	return svc
}

func TestOpenRequirementCreatesUniqueChatTaskAndExistingReturns(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement open agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92001").body(agentID), "open-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "open-first")
	if opened.Session.ID == "" || opened.Session.AgentID != agentID || opened.PendingTask == nil {
		t.Fatalf("open response = %+v", opened)
	}
	if opened.Requirement.Revision != 2 || opened.Requirement.State != "clarifying" || opened.Requirement.ChatSessionID == nil || *opened.Requirement.ChatSessionID != opened.Session.ID {
		t.Fatalf("opened requirement = %+v", opened.Requirement)
	}
	taskID := opened.PendingTask.TaskID
	var mode, requirementID string
	if err := testPool.QueryRow(context.Background(), `SELECT mode, requirement_id::text FROM agent_task_queue WHERE id = $1`, taskID).Scan(&mode, &requirementID); err != nil {
		t.Fatal(err)
	}
	if mode != "plan" || requirementID != detail.ID {
		t.Fatalf("task mode/requirement = %s %s", mode, requirementID)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, opened.Session.ID); got != 2 {
		t.Fatalf("opening/context messages = %d, want 2", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND status = 'pending'`, detail.ID); got != 0 {
		t.Fatalf("open sent a routine notification: %d", got)
	}

	existing := openRequirementForTest(t, testUserID, detail.ID, 2, "open-existing")
	if existing.Session.ID != opened.Session.ID || existing.PendingTask.TaskID != taskID {
		t.Fatalf("existing open created new entities: %+v", existing)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, opened.Session.ID); got != 1 {
		t.Fatalf("tasks after existing open = %d, want 1", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, opened.Session.ID); got != 2 {
		t.Fatalf("messages after existing open = %d, want 2", got)
	}
	replayed := openRequirementForTest(t, testUserID, detail.ID, 1, "open-first")
	if !requirementSameJSON(t, string(requirementJSON(opened)), string(requirementJSON(replayed))) {
		t.Fatalf("open replay changed response: %+v", replayed)
	}
}

func TestOpenRequirementConcurrentCreatesOneChatAndTask(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name         string
		keys         []string
		wantConflict bool
	}{
		{name: "same key", keys: []string{"same-open-key", "same-open-key"}},
		{name: "different keys", keys: []string{"open-key-a", "open-key-b"}, wantConflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, agentID := newRequirementClaudeAgent(t, "Requirement concurrent open agent")
			detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92002").body(agentID), "concurrent-open-create")
			svc := requirementServiceForTest()
			actor := service.RequirementActor{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID)}
			results := make(chan struct {
				response requirementOpenResponse
				err      error
			}, len(tc.keys))
			var wg sync.WaitGroup
			for _, key := range tc.keys {
				key := key
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := svc.OpenRequirement(context.Background(), actor, parseUUID(detail.ID), service.OpenRequirementRequest{ExpectedRevision: 1}, key, "", buildRequirementOpenForTest)
					response := requirementOpenResponse{}
					if err == nil {
						_ = json.Unmarshal(result.Body, &response)
					}
					results <- struct {
						response requirementOpenResponse
						err      error
					}{response: response, err: err}
				}()
			}
			wg.Wait()
			close(results)
			var chatID, taskID string
			conflicts := 0
			for result := range results {
				if result.err != nil {
					var requirementErr *service.RequirementError
					if !errors.As(result.err, &requirementErr) || requirementErr.Code != "revision_conflict" {
						t.Fatalf("unexpected open error: %v", result.err)
					}
					conflicts++
					continue
				}
				if chatID != "" && result.response.Session.ID != chatID {
					t.Fatalf("multiple chats created: %s and %s", chatID, result.response.Session.ID)
				}
				chatID = result.response.Session.ID
				if taskID != "" && result.response.PendingTask.TaskID != taskID {
					t.Fatalf("multiple initial tasks created: %s and %s", taskID, result.response.PendingTask.TaskID)
				}
				taskID = result.response.PendingTask.TaskID
			}
			if (tc.wantConflict && conflicts != 1) || (!tc.wantConflict && conflicts != 0) {
				t.Fatalf("conflicts = %d, want %v", conflicts, tc.wantConflict)
			}
			if chatID == "" || taskID == "" {
				t.Fatalf("missing open result: chat=%s task=%s", chatID, taskID)
			}
			registerRequirementChatCleanup(t, chatID)
			if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, chatID); got != 1 {
				t.Fatalf("chat rows = %d, want 1", got)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, chatID); got != 1 {
				t.Fatalf("initial tasks = %d, want 1", got)
			}
		})
	}
}

func TestRequirementPlanCapabilityOnlyGatesOpen(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementApproveFixture(t, "998201")
	dbfx.Exec(t, `UPDATE agent_runtime SET provider = 'codex' WHERE id = (SELECT runtime_id FROM agent WHERE id = $1)`, f.AgentID)
	detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.Detail.ID)
	if detail.Permissions.CanOpen || !detail.Permissions.CanApprove {
		t.Fatalf("Plan capability changed execute approval permissions: %+v", detail.Permissions)
	}
}

func TestOpenRequirementValidation(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("unsupported provider", func(t *testing.T) {
		runtimeID := dbfx.Runtime(t, "Requirement unsupported provider", testutil.Cols{"provider": "claude"})
		agentID := dbfx.Agent(t, "Requirement unsupported provider agent", runtimeID)
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92003").body(agentID), "open-unsupported-create")
		dbfx.Exec(t, `UPDATE agent_runtime SET provider = 'codex' WHERE id = $1`, runtimeID)
		if requirementDetailForTest(t, testUserID, testWorkspaceID, detail.ID).Permissions.CanOpen {
			t.Fatal("unsupported runtime must not offer Open")
		}
		response := testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
			map[string]any{"expectedRevision": 1}, "open-unsupported", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusConflict)
		if response.Map()["code"] != "plan_mode_not_supported" {
			t.Fatalf("provider error = %s", response.Body)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND agent_id = $2`, testWorkspaceID, agentID); got != 0 {
			t.Fatalf("chats created for unsupported provider = %d", got)
		}
	})
	t.Run("invalid source", func(t *testing.T) {
		_, agentID := newRequirementClaudeAgent(t, "Requirement invalid source open")
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92004").body(agentID), "open-invalid-source-create")
		dbfx.Exec(t, `UPDATE requirement SET source_snapshot = '{}'::jsonb, source_digest = 'invalid' WHERE id = $1`, detail.ID)
		testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
			map[string]any{"expectedRevision": 1}, "open-invalid-source", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusBadRequest)
		if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND agent_id = $2`, testWorkspaceID, agentID); got != 0 {
			t.Fatalf("chats created for invalid source = %d", got)
		}
	})
	t.Run("nonparticipant", func(t *testing.T) {
		_, agentID := newRequirementClaudeAgent(t, "Requirement nonparticipant open")
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92005").body(agentID), "open-nonparticipant-create")
		other := dbfx.User(t, "Requirement open nonparticipant", "open-nonparticipant-"+uuid.NewString()+"@example.test")
		dbfx.Member(t, testWorkspaceID, other, "member")
		testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
			other, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
			map[string]any{"expectedRevision": 1}, "open-nonparticipant", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusForbidden)
		if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND agent_id = $2`, testWorkspaceID, agentID); got != 0 {
			t.Fatalf("chats created for nonparticipant = %d", got)
		}
	})
}
