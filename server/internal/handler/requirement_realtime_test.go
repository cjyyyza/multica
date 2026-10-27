package handler

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type requirementRealtimeCapture struct {
	mu     sync.Mutex
	events []events.Event
}

func (c *requirementRealtimeCapture) record(event events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *requirementRealtimeCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *requirementRealtimeCapture) last() events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[len(c.events)-1]
}

func newRequirementRealtimeCapture(t *testing.T) *requirementRealtimeCapture {
	t.Helper()
	capture := &requirementRealtimeCapture{}
	previous := testHandler.Bus
	testHandler.Bus = events.New()
	testHandler.Bus.SubscribeAll(capture.record)
	t.Cleanup(func() { testHandler.Bus = previous })
	return capture
}

type requirementCommitStarter struct {
	pool        *pgxpool.Pool
	commit      bool
	injectedErr error
}

type requirementCommitTx struct {
	pgx.Tx
	commit      bool
	injectedErr error
}

func (s requirementCommitStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return requirementCommitTx{Tx: tx, commit: s.commit, injectedErr: s.injectedErr}, nil
}

func (t requirementCommitTx) Commit(ctx context.Context) error {
	if t.commit {
		if err := t.Tx.Commit(ctx); err != nil {
			return err
		}
	}
	return t.injectedErr
}

func TestRequirementRealtimeCommitAndReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	capture := newRequirementRealtimeCapture(t)
	_, agentID := newRequirementClaudeAgent(t, "Requirement realtime agent")
	source := newRequirementSourceFixture("95001")
	detail := createRequirementForTest(t, testUserID, source.body(agentID), "realtime-create")
	if capture.count() != 1 || capture.last().Type != "requirement:created" {
		t.Fatalf("create events = %+v", capture.events)
	}
	assertRequirementRealtimeEvent(t, capture.last(), detail.ID, 1)

	viewer := dbfx.User(t, "Requirement realtime viewer", "realtime-viewer-"+uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	updateRequirementParticipantsForTest(t, testUserID, detail.ID, requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: viewer, Roles: []string{"viewer"}}), "realtime-participants")
	if capture.count() != 2 || capture.last().Type != "requirement:updated" {
		t.Fatalf("participant events = %+v", capture.events)
	}
	assertRequirementRealtimeEvent(t, capture.last(), detail.ID, 2)

	opened := openRequirementForTest(t, testUserID, detail.ID, 2, "realtime-open")
	if capture.count() != 3 {
		t.Fatalf("open events = %+v", capture.events)
	}
	assertRequirementRealtimeEvent(t, capture.last(), detail.ID, 3)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
	candidateInput := validCandidateRequest(t, opened.Requirement, 3)
	submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, candidateInput, "realtime-candidate")
	if capture.count() != 4 {
		t.Fatalf("candidate events = %+v", capture.events)
	}
	assertRequirementRealtimeEvent(t, capture.last(), detail.ID, 4)

	createRequirementForTest(t, testUserID, source.body(agentID), "realtime-create")
	updateRequirementParticipantsForTest(t, testUserID, detail.ID, requirementParticipantsBody(1,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}},
		service.RequirementParticipantInput{UserID: viewer, Roles: []string{"viewer"}}), "realtime-participants")
	openRequirementForTest(t, testUserID, detail.ID, 2, "realtime-open")
	submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, candidateInput, "realtime-candidate")
	if capture.count() != 4 {
		t.Fatalf("replays emitted new events: %+v", capture.events)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command o JOIN requirement_event e ON o.occurrence_id = e.id WHERE o.requirement_id = $1 AND o.delivered_at IS NULL`, detail.ID); got != 3 {
		t.Fatalf("entry and candidate notification rows = %d, want 3", got)
	}

	t.Run("before commit rollback", func(t *testing.T) {
		_, agentID := newRequirementClaudeAgent(t, "Requirement before commit agent")
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("95002").body(agentID), "before-commit-create")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: errors.New("injected commit failure")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		before := capture.count()
		testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
			map[string]any{"expectedRevision": 1}, "before-commit-open", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusInternalServerError)
		if capture.count() != before {
			t.Fatalf("failed commit emitted event: %+v", capture.events)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND agent_id = $2`, testWorkspaceID, agentID); got != 0 {
			t.Fatalf("chat rows after rolled-back open = %d", got)
		}
		testHandler.TxStarter = previous
		openRequirementForTest(t, testUserID, detail.ID, 1, "before-commit-open")
	})

	t.Run("after committed response loss", func(t *testing.T) {
		_, agentID := newRequirementClaudeAgent(t, "Requirement after commit agent")
		detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("95003").body(agentID), "after-commit-create")
		previous := testHandler.TxStarter
		testHandler.TxStarter = requirementCommitStarter{pool: testPool, commit: true, injectedErr: errors.New("injected commit response loss")}
		t.Cleanup(func() { testHandler.TxStarter = previous })
		before := capture.count()
		testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
			testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
			map[string]any{"expectedRevision": 1}, "after-commit-open", "id", testWorkspaceID, "requirementId", detail.ID)).Want(http.StatusInternalServerError)
		if capture.count() != before {
			t.Fatalf("response-loss open emitted unexpected event count: %+v", capture.events)
		}
		testHandler.TxStarter = previous
		openRequirementForTest(t, testUserID, detail.ID, 1, "after-commit-open")
		if capture.count() != before {
			t.Fatalf("response-loss replay emitted new event: %+v", capture.events)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1`, detail.ID); got != 1 {
			t.Fatalf("tasks after committed replay = %d, want 1", got)
		}
	})
}

func assertRequirementRealtimeEvent(t *testing.T, event events.Event, requirementID string, revision int64) {
	t.Helper()
	if event.WorkspaceID != testWorkspaceID {
		t.Fatalf("event workspace = %s", event.WorkspaceID)
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok || payload["workspaceId"] != testWorkspaceID {
		t.Fatalf("event payload workspace = %+v", event.Payload)
	}
	if payload["requirementId"] != requirementID || payload["revision"] != revision {
		t.Fatalf("minimal event payload = %+v, want requirement %s revision %d", payload, requirementID, revision)
	}
	wantKeys := 3
	if chatSessionID, present := payload["chatSessionId"]; present {
		if chatSessionID == "" {
			t.Fatalf("event chatSessionId is empty: %+v", payload)
		}
		wantKeys++
	}
	if len(payload) != wantKeys {
		t.Fatalf("event leaks fields beyond the minimal readback pointer: %+v", payload)
	}
}

func TestRequirementFlowFeatureOffDoesNotWeakenRecoveryOrPlan(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudeAgent(t, "Requirement feature off agent")
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("95004").body(agentID), "feature-off-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "feature-off-open")
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)

	newSource := newRequirementSourceFixture("95005")
	response := testutil.Call(t, testHandler.CreateRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements", newSource.body(agentID), "feature-off-new-create",
		"id", testWorkspaceID)).Want(http.StatusForbidden)
	if response.Map()["code"] != "requirement_flow_disabled" {
		t.Fatalf("feature-off create = %s", response.Body)
	}
	existing := openRequirementForTest(t, testUserID, detail.ID, 2, "feature-off-existing-open")
	if existing.Session.ID != opened.Session.ID || existing.PendingTask.TaskID != opened.PendingTask.TaskID {
		t.Fatalf("feature-off existing open created new entities: %+v", existing)
	}
	openRequirementForTest(t, testUserID, detail.ID, 1, "feature-off-open")

	send := testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(
		testUserID, http.MethodPost, "/api/chat/sessions/"+opened.Session.ID+"/messages",
		map[string]any{"content": "Plan still applies when feature is off"}, opened.Session.ID)).Want(http.StatusCreated)
	var sendBody map[string]any
	send.JSON(&sendBody)
	taskID, _ := sendBody["task_id"].(string)
	var mode string
	if err := testPool.QueryRow(context.Background(), `SELECT mode FROM agent_task_queue WHERE id = $1`, taskID).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "plan" {
		t.Fatalf("feature-off clarification mode = %s, want plan", mode)
	}
	candidateInput := validCandidateRequest(t, opened.Requirement, 2)
	response = testutil.Call(t, testHandler.SubmitRequirementCandidate,
		candidateRequest(testUserID, agentID, opened.PendingTask.TaskID, detail.ID, opened.Session.ID, candidateInput, "feature-off-new-candidate")).Want(http.StatusForbidden)
	if response.Map()["code"] != "requirement_flow_disabled" {
		t.Fatalf("feature-off candidate = %s", response.Body)
	}
}
