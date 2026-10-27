package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

// The tests in this file exercise the task-token route policy through the real
// router built by NewRouter (see TestMain's testServer), not against a
// hand-written string table. That is the property P1-B needs: a route added to
// the production router later must be denied by default until the policy
// declares it, and the allowlists must only name routes the router actually
// serves.

type routerRoute struct {
	method  string
	pattern string
}

// realRouter returns the production router the test server is serving.
func realRouter(t *testing.T) chi.Routes {
	t.Helper()
	routes, ok := testServer.Config.Handler.(chi.Routes)
	if !ok {
		t.Fatalf("test server handler is %T, want chi.Routes", testServer.Config.Handler)
	}
	return routes
}

func walkRealRouter(t *testing.T) []routerRoute {
	t.Helper()
	var routes []routerRoute
	if err := chi.Walk(realRouter(t), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, routerRoute{method: method, pattern: route})
		return nil
	}); err != nil {
		t.Fatalf("walk real router: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("real router exposed no routes")
	}
	return routes
}

func routeExists(routes []routerRoute, method, pattern string) bool {
	for _, route := range routes {
		if route.method == method && route.pattern == pattern {
			return true
		}
	}
	return false
}

func requirementRoutes(routes []routerRoute) []routerRoute {
	var out []routerRoute
	for _, route := range routes {
		if strings.HasPrefix(route.pattern, "/api/workspaces/{id}/requirements") {
			out = append(out, route)
		}
	}
	return out
}

var routePlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// concretePath turns a Chi pattern into a request path. Values that the
// handlers parse as UUIDs are substituted with valid UUIDs so an allowed route
// reaches its handler instead of failing on parsing.
func concretePath(pattern string, replacements map[string]string) string {
	return routePlaceholder.ReplaceAllStringFunc(pattern, func(placeholder string) string {
		if value, ok := replacements[strings.Trim(placeholder, "{}")]; ok {
			return value
		}
		return uuid.NewString()
	})
}

// TestTaskPolicyAllowlistsMatchRealRouter pins the declared capabilities to the
// routes the production router actually serves. A stale declaration is
// invisible at runtime (nothing matches it) and a route that disappears from the
// router leaves a dead policy claim behind; either way the policy stops
// describing reality, so fail here instead. The daemon subtree is deliberately
// not part of this table: DaemonAuth rejects task tokens before the plan/execute
// policy runs, so a declaration there could never take effect.
func TestTaskPolicyAllowlistsMatchRealRouter(t *testing.T) {
	routes := walkRealRouter(t)

	capabilities := taskpolicy.RouteCapabilities()
	if len(capabilities) == 0 {
		t.Fatal("taskpolicy declares no route capabilities")
	}
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		key := capability.Method + " " + capability.Pattern
		if _, ok := seen[key]; ok {
			t.Errorf("task capability %s is declared more than once", key)
		}
		seen[key] = struct{}{}
		if strings.HasPrefix(capability.Pattern, "/api/daemon/") {
			t.Errorf("task capability %s names a daemon route; DaemonAuth never admits task tokens", key)
		}
		if !routeExists(routes, capability.Method, capability.Pattern) {
			t.Errorf("declared task capability %s is not a route registered by the real router", key)
		}
	}
	for _, pattern := range taskpolicy.PlanUnsafeRoutes() {
		if strings.HasPrefix(pattern, "/api/daemon/") {
			t.Errorf("plan unsafe allowlist entry %q is a daemon route; DaemonAuth never admits task tokens", pattern)
		}
		if !routeExists(routes, http.MethodPost, pattern) {
			t.Errorf("plan unsafe allowlist entry %q is not a POST route registered by the real router", pattern)
		}
	}
	for _, pattern := range taskpolicy.PlanDeniedReadRoutes() {
		if strings.HasPrefix(pattern, "/api/daemon/") {
			t.Errorf("plan denied-read allowlist entry %q is a daemon route; DaemonAuth never admits task tokens", pattern)
		}
		if !routeExists(routes, http.MethodGet, pattern) {
			t.Errorf("plan denied-read allowlist entry %q is not a GET route registered by the real router", pattern)
		}
	}
	for _, pattern := range []string{taskpolicy.RequirementBlockRoute, taskpolicy.RequirementCandidatesRoute} {
		if !routeExists(routes, http.MethodPost, pattern) {
			t.Errorf("task-token operation channel %q is not registered by the real router", pattern)
		}
	}
}

// TestTaskTokenRequirementRoutesUseRealRouterPolicy drives every requirement
// route the real router serves through the Auth middleware with an execute and
// a plan task token. It is the router-assembly counterpart to the string
// helper matrix: adding a requirement write route without declaring its mode
// fails here, because the loop reads the route list from the router itself.
func TestTaskTokenRequirementRoutesUseRealRouterPolicy(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	dbfx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtime := dbfx.Runtime(t, "Task policy router runtime")
	agent := dbfx.Agent(t, "Task policy router agent", runtime)
	chatID := dbfx.ChatSession(t, agent)
	requirementID := dbfx.Insert(t, "requirement", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"source_host":     "yixiezuo",
		"external_id":     "task-policy-router-" + uuid.NewString(),
		"source_url":      "https://dj01.pm.netease.com/issues/router-policy",
		"source_snapshot": testutil.Raw("'{}'::jsonb"),
		"source_digest":   "task-policy-router-" + uuid.NewString(),
		"requester_id":    testUserID,
		"agent_id":        agent,
		"chat_session_id": chatID,
		"intake_state":    "clarifying",
	})
	planTask := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "mode": taskpolicy.ModePlan,
		"requirement_id": requirementID, "chat_session_id": chatID,
	})
	executeTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime})
	planToken := insertTaskToken(t, dbfx, agent, planTask)
	executeToken := insertTaskToken(t, dbfx, agent, executeTask)

	replacements := map[string]string{
		"id":            testWorkspaceID,
		"requirementId": requirementID,
	}
	planWrites := taskpolicy.PlanUnsafeRoutes()

	for _, route := range requirementRoutes(walkRealRouter(t)) {
		path := concretePath(route.pattern, replacements)
		for _, token := range []struct {
			mode  string
			value string
			want  bool
		}{
			{
				mode:  taskpolicy.ModeExecute,
				value: executeToken,
				want:  !taskpolicy.IsRequirementWriteRoute(route.method, route.pattern) || route.pattern == taskpolicy.RequirementBlockRoute,
			},
			{
				mode:  taskpolicy.ModePlan,
				value: planToken,
				want:  !taskpolicy.IsRequirementWriteRoute(route.method, route.pattern) || slices.Contains(planWrites, route.pattern),
			},
		} {
			t.Run(token.mode+"/"+route.method+"/"+route.pattern, func(t *testing.T) {
				response := serveTaskToken(t, token.value, route.method, path)
				code := policyDenialCode(t, response)
				if token.want && code != "" {
					t.Fatalf("%s token was denied %s %s by route policy (%s): %s", token.mode, route.method, route.pattern, code, response.Body.String())
				}
				if !token.want && code == "" {
					t.Fatalf("%s token was allowed to call %s %s (status %d); an undeclared requirement route must be denied", token.mode, route.method, route.pattern, response.Code)
				}
			})
		}
	}
}

// TestTaskTokenUnresolvedRouteFailsClosed covers the fail-open this change
// fixes: paths matched only by a mount prefix reach Auth with an empty route
// pattern. Plan mode already rejected them; execute mode used to treat them as
// "not a requirement write" and pass the request to the router. Both modes must
// deny now, because a handler the router cannot introspect would otherwise run
// without any mode policy.
func TestTaskTokenUnresolvedRouteFailsClosed(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	dbfx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtime := dbfx.Runtime(t, "Task policy unresolved runtime")
	agent := dbfx.Agent(t, "Task policy unresolved agent", runtime)
	chatID := dbfx.ChatSession(t, agent)
	requirementID := dbfx.Insert(t, "requirement", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"source_host":     "yixiezuo",
		"external_id":     "task-policy-unresolved-" + uuid.NewString(),
		"source_snapshot": testutil.Raw("'{}'::jsonb"),
		"source_digest":   "task-policy-unresolved-" + uuid.NewString(),
		"requester_id":    testUserID,
		"agent_id":        agent,
		"chat_session_id": chatID,
		"intake_state":    "clarifying",
	})
	executeTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime})
	planTask := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "mode": taskpolicy.ModePlan,
		"requirement_id": requirementID, "chat_session_id": chatID,
	})
	executeToken := insertTaskToken(t, dbfx, agent, executeTask)
	planToken := insertTaskToken(t, dbfx, agent, planTask)

	paths := []struct {
		name   string
		method string
		path   string
	}{
		{"unregistered requirement leaf", http.MethodPost, "/api/workspaces/" + testWorkspaceID + "/requirements/" + uuid.NewString() + "/not-a-route"},
		{"unregistered requirement read", http.MethodGet, "/api/workspaces/" + testWorkspaceID + "/requirements/" + uuid.NewString() + "/not-a-route"},
		{"unregistered workspace leaf", http.MethodPost, "/api/workspaces/" + testWorkspaceID + "/not-a-route"},
		{"unregistered issue leaf", http.MethodPost, "/api/issues/" + uuid.NewString() + "/not-a-route"},
	}
	for _, tc := range paths {
		for _, token := range []struct {
			mode     string
			value    string
			wantCode string
		}{
			{taskpolicy.ModeExecute, executeToken, "requirement_task_action_denied"},
			{taskpolicy.ModePlan, planToken, "plan_mode_tool_denied"},
		} {
			t.Run(token.mode+"/"+tc.name, func(t *testing.T) {
				response := serveTaskToken(t, token.value, tc.method, tc.path)
				code := policyDenialCode(t, response)
				if response.Code != http.StatusForbidden || code != token.wantCode {
					t.Fatalf("%s %s = %d %q, want 403 %s: %s", tc.method, tc.path, response.Code, code, token.wantCode, response.Body.String())
				}
			})
		}
	}
}

// TestTaskTokenKnownRoutesKeepWorking makes sure fail-closed only closes the
// unknown cases: an execute task token can still write ordinary resources, and
// a plan task token can still read them.
func TestTaskTokenKnownRoutesKeepWorking(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	dbfx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtime := dbfx.Runtime(t, "Task policy known runtime")
	agent := dbfx.Agent(t, "Task policy known agent", runtime)
	executeTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime})
	executeToken := insertTaskToken(t, dbfx, agent, executeTask)

	response := serveTaskToken(t, executeToken, http.MethodPost, "/api/issues")
	if code := policyDenialCode(t, response); code != "" {
		t.Fatalf("execute task token was denied the ordinary issue write: %s", response.Body.String())
	}

	chatID := dbfx.ChatSession(t, agent)
	requirementID := dbfx.Insert(t, "requirement", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"source_host":     "yixiezuo",
		"external_id":     "task-policy-plan-read-" + uuid.NewString(),
		"source_snapshot": testutil.Raw("'{}'::jsonb"),
		"source_digest":   "task-policy-plan-read-" + uuid.NewString(),
		"requester_id":    testUserID,
		"agent_id":        agent,
		"chat_session_id": chatID,
		"intake_state":    "clarifying",
	})
	planTask := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "mode": taskpolicy.ModePlan,
		"requirement_id": requirementID, "chat_session_id": chatID,
	})
	planToken := insertTaskToken(t, dbfx, agent, planTask)
	response = serveTaskToken(t, planToken, http.MethodGet, "/api/issues")
	if code := policyDenialCode(t, response); code != "" {
		t.Fatalf("plan task token was denied an ordinary read: %s", response.Body.String())
	}
}

func TestTaskTokenPlanCannotCancelOtherTasks(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	dbfx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtime := dbfx.Runtime(t, "Plan cancellation runtime")
	agent := dbfx.Agent(t, "Plan cancellation agent", runtime)
	chatID := dbfx.ChatSession(t, agent)
	requirementID := dbfx.Insert(t, "requirement", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"source_host":     "yixiezuo",
		"external_id":     "task-policy-plan-cancel-" + uuid.NewString(),
		"source_snapshot": testutil.Raw("'{}'::jsonb"),
		"source_digest":   "task-policy-plan-cancel-" + uuid.NewString(),
		"requester_id":    testUserID,
		"agent_id":        agent,
		"chat_session_id": chatID,
		"intake_state":    "clarifying",
	})
	planTask := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "mode": taskpolicy.ModePlan,
		"requirement_id": requirementID, "chat_session_id": chatID,
	})
	planToken := insertTaskToken(t, dbfx, agent, planTask)

	issueID := dbfx.Issue(t, "Plan cancellation issue")
	issueTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issueID, "status": "queued"})
	response := serveTaskToken(t, planToken, http.MethodPost, "/api/issues/"+issueID+"/tasks/"+issueTask+"/cancel")
	if response.Code != http.StatusForbidden || policyDenialCode(t, response) != "plan_mode_tool_denied" {
		t.Fatalf("issue task cancellation = %d: %s", response.Code, response.Body.String())
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'queued'`, issueTask); got != 1 {
		t.Fatalf("denied cancellation changed issue task status, queued count = %d", got)
	}

	idIssue := dbfx.Issue(t, "Plan id-only cancellation issue")
	idTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": idIssue, "status": "queued"})
	response = serveTaskToken(t, planToken, http.MethodPost, "/api/tasks/"+idTask+"/cancel")
	if response.Code != http.StatusForbidden || policyDenialCode(t, response) != "plan_mode_tool_denied" {
		t.Fatalf("id-only task cancellation = %d: %s", response.Code, response.Body.String())
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND status = 'queued'`, idTask); got != 1 {
		t.Fatalf("denied cancellation changed id-only task status, queued count = %d", got)
	}
}

// TestExecutePolicyResolvesEveryRegisteredRoute guards the usability side of
// the fail-closed change. Execute mode has no hand-written route allowlist: it
// allows whatever route the router resolves and restricts only requirement
// writes (to block). If a future registration shape stops resolving through
// Find, the Auth middleware would deny legitimate agent traffic; fail here
// first by proving every registered route resolves and lands on the expected
// execute decision.
func TestExecutePolicyResolvesEveryRegisteredRoute(t *testing.T) {
	routes := walkRealRouter(t)
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		key := route.method + " " + route.pattern
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		path := concretePath(route.pattern, map[string]string{})
		resolved := realRouter(t).Find(chi.NewRouteContext(), route.method, path)
		if resolved == "" {
			t.Errorf("registered route %s does not resolve through Find (path %q): execute task tokens would be denied", key, path)
			continue
		}
		want := !taskpolicy.IsRequirementWriteRoute(route.method, resolved) || resolved == taskpolicy.RequirementBlockRoute
		if got := taskpolicy.ExecuteRouteAllowed(route.method, resolved); got != want {
			t.Errorf("route %s resolved to %q: ExecuteRouteAllowed = %v, want %v", key, resolved, got, want)
		}
	}
}

func insertTaskToken(t *testing.T, dbfx *testutil.Fixture, agentID, taskID string) string {
	t.Helper()
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatalf("generate task token: %v", err)
	}
	dbfx.Insert(t, "task_token", testutil.Cols{
		"token_hash":   auth.HashToken(token),
		"task_id":      taskID,
		"agent_id":     agentID,
		"workspace_id": testWorkspaceID,
		"user_id":      testUserID,
		"expires_at":   time.Now().Add(time.Hour),
	})
	return token
}

func serveTaskToken(t *testing.T, token, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	testServer.Config.Handler.ServeHTTP(response, request)
	return response
}

// policyDenialCode returns the task-policy denial code in the response, or the
// empty string when the request was not stopped by the route policy. Other
// 403s (handler authorization) are not policy denials and must stay distinct.
func policyDenialCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		return ""
	}
	switch body.Code {
	case "plan_mode_tool_denied", "requirement_task_action_denied":
		return body.Code
	default:
		return ""
	}
}
