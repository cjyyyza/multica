package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

type planTaskFixture struct {
	RuntimeID     string
	AgentID       string
	ChatID        string
	RequirementID string
	TaskID        string
}

func newPlanTaskFixture(t *testing.T, provider string, withPlanCapability bool) planTaskFixture {
	t.Helper()
	enableRequirementFlowForTest(t)
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	daemonID := "plan-daemon-" + uuid.NewString()
	metadata := testutil.Raw("'{}'::jsonb")
	if withPlanCapability {
		metadata = testutil.Raw(`'{"capabilities":["plan-mode-v1"]}'::jsonb`)
	}
	runtimeID := dbfx.Runtime(t, "Plan runtime "+uuid.NewString(), testutil.Cols{
		"daemon_id": daemonID, "provider": provider, "metadata": metadata,
	})
	agentID := dbfx.Agent(t, "Plan agent "+uuid.NewString(), runtimeID, testutil.Cols{
		"permission_mode": "public_to",
		"mcp_config":      testutil.Raw(`'{"mcpServers":{"private":{"url":"https://private.example"}}}'::jsonb`),
		"custom_args":     testutil.Raw(`'["--permission-mode","bypassPermissions"]'::jsonb`),
	})
	dbfx.InsertNoID(t, "agent_invocation_target", testutil.Cols{
		"agent_id": agentID, "target_type": "workspace", "target_id": testWorkspaceID, "created_by": testUserID,
	}, "agent_id = $1 AND target_type = $2 AND target_id = $3", agentID, "workspace", testWorkspaceID)
	requirement := createRequirementForTest(t, testUserID, newRequirementSourceFixture(fmt.Sprint(time.Now().UnixNano())).body(agentID), "plan-"+uuid.NewString())
	chatID := dbfx.ChatSession(t, agentID)
	dbfx.Exec(t, `UPDATE requirement SET chat_session_id = $1 WHERE id = $2`, chatID, requirement.ID)
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(chatID))
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	sent, err := testHandler.TaskService.SendDirectChatMessage(ctx, session, agent, parseUUID(testUserID), "clarify the requirement", nil, "member", parseUUID(testUserID))
	if err != nil {
		t.Fatalf("send requirement chat: %v", err)
	}
	if sent.Task.Mode != taskpolicy.ModePlan || utilUUIDString(sent.Task.RequirementID) != requirement.ID {
		t.Fatalf("task mode/requirement = %q/%q, want plan/%s", sent.Task.Mode, utilUUIDString(sent.Task.RequirementID), requirement.ID)
	}
	return planTaskFixture{RuntimeID: runtimeID, AgentID: agentID, ChatID: chatID, RequirementID: requirement.ID, TaskID: utilUUIDString(sent.Task.ID)}
}

func utilUUIDString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func planClaimRequest(t *testing.T, fixture planTaskFixture, withCapability bool) *http.Request {
	t.Helper()
	var daemonID string
	if err := testPool.QueryRow(context.Background(), `SELECT daemon_id FROM agent_runtime WHERE id = $1`, fixture.RuntimeID).Scan(&daemonID); err != nil {
		t.Fatalf("load daemon id: %v", err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+fixture.RuntimeID+"/tasks/claim", nil, testWorkspaceID, daemonID)
	req = withURLParam(req, "runtimeId", fixture.RuntimeID)
	if withCapability {
		req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityPlanModeV1)
	}
	return req
}

func claimPlanResponse(t *testing.T, req *http.Request) (AgentTaskResponse, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("claim status = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if body.Task == nil {
		return AgentTaskResponse{}, w
	}
	return *body.Task, w
}

func TestRequirementChatDerivesPlanAndRetryInheritsIt(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	ctx := context.Background()
	parentID := parseUUID(fixture.TaskID)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'failed', completed_at = now(), failure_reason = 'agent_error.provider_network', attempt = 1, max_attempts = 3 WHERE id = $1`, parentID); err != nil {
		t.Fatal(err)
	}
	child, err := testHandler.Queries.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: parentID})
	if err != nil {
		t.Fatalf("retry plan task: %v", err)
	}
	if child.Mode != taskpolicy.ModePlan || utilUUIDString(child.RequirementID) != fixture.RequirementID {
		t.Fatalf("retry mode/requirement = %q/%q", child.Mode, utilUUIDString(child.RequirementID))
	}
	issueID := dbfx.Issue(t, "Approved requirement issue")
	dbfx.Exec(t, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, fixture.RequirementID)
	if _, err := testHandler.Queries.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: parentID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry after Issue binding must be refused, got %v", err)
	}
}

func TestRequirementChatStopsAfterIssueAndCancellation(t *testing.T) {
	for _, terminal := range []string{"issue", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			fixture := newPlanTaskFixture(t, "claude", true)
			issueID := dbfx.Issue(t, "Requirement terminal issue")
			if terminal == "issue" {
				dbfx.Exec(t, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, fixture.RequirementID)
			} else {
				dbfx.Exec(t, `UPDATE requirement SET cancelled_at = now(), cancel_reason = 'test cancellation' WHERE id = $1`, fixture.RequirementID)
			}
			ctx := context.Background()
			session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(fixture.ChatID))
			if err != nil {
				t.Fatal(err)
			}
			agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(fixture.AgentID))
			if err != nil {
				t.Fatal(err)
			}
			var taskCount int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, fixture.ChatID).Scan(&taskCount); err != nil {
				t.Fatal(err)
			}
			_, err = testHandler.TaskService.SendDirectChatMessage(ctx, session, agent, parseUUID(testUserID), "must not execute", nil, "member", parseUUID(testUserID))
			var gateErr *service.RequirementChatExecutionError
			if !errors.As(err, &gateErr) {
				t.Fatalf("send after %s error = %v, want requirement gate", terminal, err)
			}
			if terminal == "issue" && (gateErr.Code != service.RequirementChatIssueExistsCode || gateErr.IssueID != issueID) {
				t.Fatalf("gate error = %+v, want existing issue %s", gateErr, issueID)
			}
			if terminal == "cancelled" && gateErr.Code != service.RequirementCancelledCode {
				t.Fatalf("gate error = %+v, want cancelled", gateErr)
			}
			var afterCount int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, fixture.ChatID).Scan(&afterCount); err != nil {
				t.Fatal(err)
			}
			if afterCount != taskCount {
				t.Fatalf("blocked send created a task: before=%d after=%d", taskCount, afterCount)
			}
			_, _, quickErr := testHandler.TaskService.RegenerateChatQuickActions(ctx, session, parseUUID(uuid.NewString()))
			if !errors.As(quickErr, &gateErr) {
				t.Fatalf("quick actions after %s error = %v, want requirement gate", terminal, quickErr)
			}
			w := httptest.NewRecorder()
			if !writeRequirementChatExecutionError(w, err) {
				t.Fatal("gate error was not mapped")
			}
			if w.Code != http.StatusConflict || (terminal == "issue" && !strings.Contains(w.Body.String(), "issue_id")) {
				t.Fatalf("gate response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPlanClaimStripsToolsAndTaskTokenPolicy(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	claimed, _ := claimPlanResponse(t, planClaimRequest(t, fixture, true))
	if claimed.ID != fixture.TaskID || claimed.Mode != taskpolicy.ModePlan || claimed.RequirementID != fixture.RequirementID {
		t.Fatalf("claim identity = %+v", claimed)
	}
	if claimed.PlanPermission != taskpolicy.PlanPermissionClaude || claimed.Agent == nil {
		t.Fatalf("plan permission/agent = %+v", claimed)
	}
	if len(claimed.PluginHookTools) != 0 || len(claimed.RemoteMCPConnections) != 0 || len(claimed.ConnectedApps) != 0 || claimed.RemoteMCPDaemonToken != "" {
		t.Fatalf("external tools reached plan claim: %+v", claimed)
	}
	if len(claimed.Agent.CustomArgs) != 0 || !strings.Contains(string(claimed.Agent.McpConfig), `"mcpServers":{}`) {
		t.Fatalf("agent tools were not stripped: args=%v mcp=%s", claimed.Agent.CustomArgs, claimed.Agent.McpConfig)
	}
	if claimed.AuthToken == "" {
		t.Fatal("plan claim did not mint a task token")
	}
	tokenRow, err := testHandler.Queries.GetTaskTokenByHash(context.Background(), taskTokenHash(claimed.AuthToken))
	if err != nil {
		t.Fatalf("load task token: %v", err)
	}
	if tokenRow.Mode != taskpolicy.ModePlan || utilUUIDString(tokenRow.RequirementID) != fixture.RequirementID || utilUUIDString(tokenRow.ChatSessionID) != fixture.ChatID || tokenRow.IssueID.Valid {
		t.Fatalf("task token scope = %+v", tokenRow)
	}

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil, router))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	router.Post("/api/workspaces/{id}/requirements/{requirementId}/candidates", ok)
	router.Post("/api/issues/{id}/tasks/{taskId}/cancel", ok)
	router.Post("/api/tasks/{taskId}/cancel", ok)
	router.Get("/api/issues/{id}", ok)
	router.Post("/api/issues", ok)
	router.Post("/api/issues/quick-create", ok)
	router.Post("/api/issues/{id}/comments", ok)
	router.Post("/api/issues/{id}/wakeups", ok)
	router.Post("/api/chat/sessions/{sessionId}/messages", ok)
	router.Post("/api/workspaces/{id}/yixiezuo/pm/issues", ok)
	router.Post("/api/workspaces/{id}/popo/install", ok)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/workspaces/ws/requirements/req/candidates", http.StatusNoContent},
		{http.MethodPost, "/api/issues/issue/tasks/task/cancel", http.StatusForbidden},
		{http.MethodPost, "/api/tasks/task/cancel", http.StatusForbidden},
		{http.MethodGet, "/api/issues/issue", http.StatusNoContent},
		{http.MethodPost, "/api/issues", http.StatusForbidden},
		{http.MethodPost, "/api/issues/quick-create", http.StatusForbidden},
		{http.MethodPost, "/api/issues/issue/comments", http.StatusForbidden},
		{http.MethodPost, "/api/issues/issue/wakeups", http.StatusForbidden},
		{http.MethodPost, "/api/chat/sessions/chat/messages", http.StatusForbidden},
		{http.MethodPost, "/api/workspaces/ws/yixiezuo/pm/issues", http.StatusForbidden},
		{http.MethodPost, "/api/workspaces/ws/popo/install", http.StatusForbidden},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+claimed.AuthToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}

	// /api/daemon never accepts a task token: DaemonAuth has no mat_ branch, so
	// the plan/execute route policy is not even consulted there. Test the real
	// boundary instead of mounting daemon routes under Auth, where they would
	// only prove a policy that production cannot reach.
	daemonRouter := chi.NewRouter()
	daemonRouter.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(testHandler.Queries, nil, nil, nil))
		r.Post("/tasks/{taskId}/complete", ok)
		r.Post("/tasks/{id}/plugin-hooks", ok)
		r.Get("/tasks/{id}/plugin-mcp/{contributionId}/credential", ok)
	})
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/daemon/tasks/task/complete"},
		{http.MethodPost, "/api/daemon/tasks/task/plugin-hooks"},
		{http.MethodGet, "/api/daemon/tasks/task/plugin-mcp/contrib/credential"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+claimed.AuthToken)
		w := httptest.NewRecorder()
		daemonRouter.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s through DaemonAuth = %d, want 401 (%s)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestPlanClaimFailsClosedForOldDaemonAndUnsupportedProvider(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claimProvider string
		daemonCap     bool
		requestCap    bool
		customProfile bool
		wantError     string
	}{
		{"old daemon", "", false, false, false, "plan-mode-v1"},
		{"unsupported provider", "codex", true, true, false, "built-in Claude"},
		{"custom claude profile", "", true, true, true, "built-in Claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Requirement create and open already reject a runtime that cannot run
			// Plan Mode, so the fixture opens with a supported Claude runtime and
			// the claim-time cases change the runtime before the daemon asks for
			// the task.
			fixture := newPlanTaskFixture(t, "claude", tc.daemonCap)
			if tc.claimProvider != "" {
				dbfx.Exec(t, `UPDATE agent_runtime SET provider = $1 WHERE id = $2`, tc.claimProvider, fixture.RuntimeID)
			}
			if tc.customProfile {
				profileID := insertRuntimeProfileFixture(t, context.Background(), "Unverified Claude profile", "claude", "missing-claude")
				dbfx.Exec(t, `UPDATE agent_runtime SET profile_id = $1 WHERE id = $2`, profileID, fixture.RuntimeID)
			}
			claimed, _ := claimPlanResponse(t, planClaimRequest(t, fixture, tc.requestCap))
			if claimed.ID != "" {
				t.Fatalf("unsupported plan task was delivered: %+v", claimed)
			}
			var status, message string
			if err := testPool.QueryRow(context.Background(), `SELECT status, error FROM agent_task_queue WHERE id = $1`, fixture.TaskID).Scan(&status, &message); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || !strings.Contains(message, tc.wantError) {
				t.Fatalf("task = %q %q, want failed containing %q", status, message, tc.wantError)
			}
			var tokens int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_token WHERE task_id = $1`, fixture.TaskID).Scan(&tokens); err != nil {
				t.Fatal(err)
			}
			if tokens != 0 {
				t.Fatalf("rejected claim minted %d task tokens", tokens)
			}
		})
	}
}

func TestExecuteClaimAndTaskTokenBehaviorUnchanged(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Execute regression runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Execute regression agent")
	dbfx.Exec(t, `UPDATE agent SET mcp_config = '{"mcpServers":{"private":{"url":"https://private.example"}}}' WHERE id = $1`, agentID)
	taskID := seedQueuedIssueTask(t, ctx, agentID, runtimeID, issueID)
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "execute-regression")
	req = withURLParam(req, "runtimeId", runtimeID)
	claimed, _ := claimPlanResponse(t, req)
	if claimed.ID != taskID || claimed.Mode != taskpolicy.ModeExecute || claimed.RequirementID != "" || claimed.AuthToken == "" {
		t.Fatalf("execute claim = %+v", claimed)
	}
	if !strings.Contains(string(claimed.Agent.McpConfig), "private.example") {
		t.Fatalf("execute MCP config regressed: %s", claimed.Agent.McpConfig)
	}
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil, router))
	router.Post("/api/issues", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	writeReq := httptest.NewRequest(http.MethodPost, "/api/issues", nil)
	writeReq.Header.Set("Authorization", "Bearer "+claimed.AuthToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, writeReq)
	if w.Code != http.StatusNoContent {
		t.Fatalf("execute task-token write = %d, want 204: %s", w.Code, w.Body.String())
	}
}

func TestEnqueuePreparedRequirementChatTaskInTxIsReusableByOpen(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	ctx := context.Background()
	prepared, err := testHandler.TaskService.PrepareChatTaskEnqueue(ctx, parseUUID(fixture.AgentID), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("prepare open primitive: %v", err)
	}
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(fixture.ChatID))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	task, err := testHandler.TaskService.EnqueuePreparedRequirementChatTaskInTx(ctx, tx, session, parseUUID(testUserID), prepared)
	if err != nil {
		t.Fatalf("open primitive: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if task.ID == parseUUID(fixture.TaskID) || task.Mode != taskpolicy.ModePlan || utilUUIDString(task.RequirementID) != fixture.RequirementID {
		t.Fatalf("open primitive task = %+v", task)
	}
}

func TestPlanPluginHookAndCredentialInvocationAreDenied(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.PluginsV1, featureflag.Rule{Default: true})
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	var daemonID string
	if err := testPool.QueryRow(context.Background(), `SELECT daemon_id FROM agent_runtime WHERE id = $1`, fixture.RuntimeID).Scan(&daemonID); err != nil {
		t.Fatal(err)
	}
	hookReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+fixture.TaskID+"/plugin-hooks", map[string]any{
		"installation_id": uuid.NewString(), "hook_key": "write", "input": map[string]any{},
	}, testWorkspaceID, daemonID)
	hookReq = withURLParam(hookReq, "id", fixture.TaskID)
	hookResp := httptest.NewRecorder()
	testHandler.InvokeAgentPluginHook(hookResp, hookReq)
	if hookResp.Code != http.StatusForbidden || !strings.Contains(hookResp.Body.String(), taskpolicy.PlanModeToolDenied) {
		t.Fatalf("plugin hook response = %d %s", hookResp.Code, hookResp.Body.String())
	}

	credentialReq := newDaemonTokenRequest(http.MethodGet, "/api/daemon/tasks/"+fixture.TaskID+"/plugin-mcp/plugin:install:write/credential", nil, testWorkspaceID, daemonID)
	credentialReq = testutil.WithURLParams(credentialReq, "id", fixture.TaskID, "contributionId", "plugin:install:write")
	credentialResp := httptest.NewRecorder()
	testHandler.ResolvePluginMCPCredential(credentialResp, credentialReq)
	if credentialResp.Code != http.StatusForbidden || !strings.Contains(credentialResp.Body.String(), taskpolicy.PlanModeToolDenied) {
		t.Fatalf("plugin credential response = %d %s", credentialResp.Code, credentialResp.Body.String())
	}
}

func TestRequirementIssueRerunRetainsExecuteBindingAndCancelBlocks(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	ctx := context.Background()
	issueID := dbfx.Issue(t, "Requirement execute issue", testutil.Cols{"assignee_type": "agent", "assignee_id": fixture.AgentID})
	dbfx.Exec(t, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, fixture.RequirementID)
	sourceID := dbfx.Task(t, fixture.AgentID, testutil.Cols{
		"runtime_id": fixture.RuntimeID, "issue_id": issueID, "mode": taskpolicy.ModeExecute,
		"requirement_id": fixture.RequirementID, "status": "completed", "completed_at": testutil.Raw("now()"),
	})
	rerun, err := testHandler.TaskService.RerunIssue(ctx, parseUUID(issueID), parseUUID(sourceID), pgtype.UUID{}, parseUUID(testUserID), nil)
	if err != nil {
		t.Fatalf("rerun requirement issue: %v", err)
	}
	if rerun.Mode != taskpolicy.ModeExecute || utilUUIDString(rerun.RequirementID) != fixture.RequirementID {
		t.Fatalf("rerun mode/requirement = %q/%q", rerun.Mode, utilUUIDString(rerun.RequirementID))
	}
	var before int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE requirement SET cancelled_at = now() WHERE id = $1`, fixture.RequirementID)
	if _, err := testHandler.TaskService.RerunIssue(ctx, parseUUID(issueID), parseUUID(sourceID), pgtype.UUID{}, parseUUID(testUserID), nil); err == nil {
		t.Fatal("cancelled requirement rerun was accepted")
	}
	var after int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("cancelled rerun created work: before=%d after=%d", before, after)
	}
}

func TestValidateTaskForRequirementCandidate(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	ctx := context.Background()
	if _, err := testHandler.TaskService.ValidateTaskForRequirementCandidate(ctx, parseUUID(fixture.TaskID), parseUUID(fixture.RequirementID), parseUUID(testWorkspaceID)); !errors.Is(err, service.ErrTaskRequirementBinding) {
		t.Fatalf("queued candidate task was accepted: %v", err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, fixture.TaskID)
	task, err := testHandler.TaskService.ValidateTaskForRequirementCandidate(ctx, parseUUID(fixture.TaskID), parseUUID(fixture.RequirementID), parseUUID(testWorkspaceID))
	if err != nil || utilUUIDString(task.ID) != fixture.TaskID {
		t.Fatalf("running candidate validation = task %+v err %v", task, err)
	}
	issueID := dbfx.Issue(t, "Candidate closing issue")
	dbfx.Exec(t, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, fixture.RequirementID)
	if _, err := testHandler.TaskService.ValidateTaskForRequirementCandidate(ctx, parseUUID(fixture.TaskID), parseUUID(fixture.RequirementID), parseUUID(testWorkspaceID)); err == nil {
		t.Fatal("candidate validation remained open after Issue binding")
	}
}

func TestSendChatMessageAfterIssueGuidesToExistingIssue(t *testing.T) {
	fixture := newPlanTaskFixture(t, "claude", true)
	issueID := dbfx.Issue(t, "Existing requirement issue")
	dbfx.Exec(t, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, fixture.RequirementID)
	req := newRequest(http.MethodPost, "/api/chat/sessions/"+fixture.ChatID+"/messages", map[string]any{"content": "start another run"})
	req = chatPendingCtxAs(t, withURLParam(req, "sessionId", fixture.ChatID), testUserID)
	w := httptest.NewRecorder()
	testHandler.SendChatMessage(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("send response = %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != service.RequirementChatIssueExistsCode || body["issue_id"] != issueID {
		t.Fatalf("send response does not guide to the existing Issue: %s", w.Body.String())
	}
}

func taskTokenHash(raw string) string {
	return auth.HashToken(raw)
}
