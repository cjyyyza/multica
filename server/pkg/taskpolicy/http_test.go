package taskpolicy

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestNormalizeMode(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "execute", " EXECUTE "} {
		got, err := NormalizeMode(input)
		if err != nil || got != ModeExecute {
			t.Fatalf("NormalizeMode(%q) = %q, %v", input, got, err)
		}
	}
	got, err := NormalizeMode("plan")
	if err != nil || got != ModePlan {
		t.Fatalf("NormalizeMode(plan) = %q, %v", got, err)
	}
	if _, err := NormalizeMode("full-access"); err == nil {
		t.Fatal("unknown mode must fail closed")
	}
}

func TestPlanToolRouteMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		method  string
		pattern string
		allowed bool
	}{
		{"candidate is the plan task's requirement write", http.MethodPost, RequirementCandidatesRoute, true},
		{"candidate other methods are not implicitly allowed", http.MethodPut, RequirementCandidatesRoute, false},
		{"block belongs to execute tasks", http.MethodPost, RequirementBlockRoute, false},
		{"plan cannot cancel issue tasks", http.MethodPost, IssueTaskCancelRoute, false},
		{"plan cannot cancel tasks", http.MethodPost, TaskCancelRoute, false},
		{"task cancellation other methods are not implicitly allowed", http.MethodPut, TaskCancelRoute, false},
		{"daemon task completion is not a plan capability", http.MethodPost, "/api/daemon/tasks/{taskId}/complete", false},
		{"daemon plugin hook is not a plan capability", http.MethodPost, "/api/daemon/tasks/{id}/plugin-hooks", false},
		{"ordinary issue create", http.MethodPost, "/api/issues", false},
		{"ordinary issue update", http.MethodPut, "/api/issues/{id}", false},
		{"issue comment", http.MethodPost, "/api/issues/{id}/comments", false},
		{"quick create", http.MethodPost, "/api/issues/quick-create", false},
		{"chat starts a new execution", http.MethodPost, "/api/chat/sessions/{sessionId}/messages", false},
		{"chat quick actions", http.MethodPost, "/api/chat/sessions/{sessionId}/quick-actions/regenerate", false},
		{"issue rerun", http.MethodPost, "/api/issues/{id}/rerun", false},
		{"wakeup create", http.MethodPost, "/api/issues/{id}/wakeups", false},
		{"wakeup trigger", http.MethodPost, "/api/issues/{id}/wakeups/{wakeupID}/trigger", false},
		{"yixiezuo write", http.MethodPost, "/api/workspaces/{id}/yixiezuo/pm/issues", false},
		{"yixiezuo operation completion", http.MethodPost, "/api/workspaces/{id}/yixiezuo/operations/{operationId}/complete", false},
		{"popo install", http.MethodPost, "/api/workspaces/{id}/popo/install", false},
		{"popo bridge command receipt", http.MethodPost, "/api/popo/bridge/commands/{id}/receipt", false},
		{"plugin action cannot bypass task token", http.MethodPost, "/api/plugin-bridge/v1/hooks/{key}", false},
		{"p4 result is not generic lifecycle", http.MethodPost, "/api/daemon/tasks/{taskId}/p4/changelists", false},
		{"p4 status write", http.MethodPost, "/api/daemon/p4/changelists/status", false},
		{"other business write", http.MethodPost, "/api/agents/{id}/mcp-servers", false},
		{"ordinary read", http.MethodGet, "/api/issues/{id}", true},
		{"head is a safe read", http.MethodHead, "/api/issues/{id}", true},
		{"options is a safe read", http.MethodOptions, "/api/issues/{id}", true},
		{"plugin surface launch read denied", http.MethodGet, PluginSurfaceLaunchRoute, false},
		{"plugin surface launch head denied", http.MethodHead, PluginSurfaceLaunchRoute, false},
		{"plugin surface launch options denied", http.MethodOptions, PluginSurfaceLaunchRoute, false},
		{"unmatched route fails closed", http.MethodPost, "", false},
		{"unmatched read fails closed", http.MethodGet, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanRouteAllowed(tc.method, tc.pattern); got != tc.allowed {
				t.Fatalf("PlanRouteAllowed(%s, %q) = %v, want %v", tc.method, tc.pattern, got, tc.allowed)
			}
		})
	}
}

func TestRequirementTaskWriteRouteMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		method  string
		pattern string
		allowed bool
	}{
		{"block is the execute task write capability", http.MethodPost, RequirementBlockRoute, true},
		{"accept is not a task capability", http.MethodPost, "/api/workspaces/{id}/requirements/{requirementId}/accept", false},
		{"candidate is not an execute capability", http.MethodPost, RequirementCandidatesRoute, false},
		{"block other methods are not allowed", http.MethodPut, RequirementBlockRoute, false},
		{"task cancellation is not a requirement write", http.MethodPost, TaskCancelRoute, false},
		{"unresolved route is not a write capability", http.MethodPost, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequirementTaskWriteRouteAllowed(tc.method, tc.pattern); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
		})
	}
	if IsRequirementWriteRoute(http.MethodGet, RequirementBlockRoute) {
		t.Fatal("read route was treated as a business write")
	}
	if IsRequirementWriteRoute(http.MethodPost, "/api/issues/{id}") {
		t.Fatal("ordinary issue write was treated as a requirement action")
	}
	if PlanRouteAllowed(http.MethodPost, RequirementBlockRoute) {
		t.Fatal("plan task was allowed to block an approved requirement issue")
	}
}

func TestExecuteRouteMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		method  string
		pattern string
		allowed bool
	}{
		{"unresolved route fails closed", http.MethodPost, "", false},
		{"unresolved read fails closed", http.MethodGet, "", false},
		{"block is the execute requirement capability", http.MethodPost, RequirementBlockRoute, true},
		{"accept is not an execute capability", http.MethodPost, "/api/workspaces/{id}/requirements/{requirementId}/accept", false},
		{"open is not an execute capability", http.MethodPost, "/api/workspaces/{id}/requirements/{requirementId}/open", false},
		{"approve is not an execute capability", http.MethodPost, "/api/workspaces/{id}/requirements/{requirementId}/approve", false},
		{"candidate stays with plan tasks", http.MethodPost, RequirementCandidatesRoute, false},
		{"creating a requirement is not an execute capability", http.MethodPost, "/api/workspaces/{id}/requirements", false},
		{"a requirement write route must be declared before execute may use it", http.MethodPost, "/api/workspaces/{id}/requirements/{requirementId}/archive", false},
		{"ordinary issue write", http.MethodPost, "/api/issues/{id}/comments", true},
		{"ordinary task write", http.MethodPost, "/api/issues", true},
		{"ordinary read", http.MethodGet, "/api/issues/{id}", true},
		{"requirement read", http.MethodGet, "/api/workspaces/{id}/requirements/{requirementId}", true},
		{"unknown method on an ordinary route", http.MethodDelete, "/api/issues/{id}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecuteRouteAllowed(tc.method, tc.pattern); got != tc.allowed {
				t.Fatalf("ExecuteRouteAllowed(%s, %q) = %v, want %v", tc.method, tc.pattern, got, tc.allowed)
			}
		})
	}
}

func TestRouteAllowlistsAreExplicitAndStable(t *testing.T) {
	t.Parallel()
	unsafe := PlanUnsafeRoutes()
	if !slices.IsSorted(unsafe) || len(unsafe) == 0 {
		t.Fatalf("plan unsafe routes must be sorted and non-empty: %v", unsafe)
	}
	for _, pattern := range unsafe {
		if strings.HasPrefix(pattern, "/api/daemon/") {
			t.Errorf("plan unsafe route %q is guarded by DaemonAuth and cannot be reached with a task token", pattern)
		}
	}
	if want := []string{RequirementCandidatesRoute}; !slices.Equal(unsafe, want) {
		t.Fatalf("plan unsafe routes = %v, want %v", unsafe, want)
	}

	deniedReads := PlanDeniedReadRoutes()
	if !slices.IsSorted(deniedReads) {
		t.Fatalf("plan denied reads must be sorted: %v", deniedReads)
	}
	if want := []string{PluginSurfaceLaunchRoute}; !slices.Equal(deniedReads, want) {
		t.Fatalf("plan denied reads = %v, want %v", deniedReads, want)
	}

	// The returned slices are copies: callers must not be able to widen policy.
	unsafe[0] = "/api/issues"
	if PlanRouteAllowed(http.MethodPost, "/api/issues") {
		t.Fatal("PlanUnsafeRoutes exposed the internal allowlist")
	}
	deniedReads[0] = "/api/issues/{id}"
	if !PlanRouteAllowed(http.MethodGet, "/api/issues/{id}") {
		t.Fatal("PlanDeniedReadRoutes exposed the internal denylist")
	}
}

func TestRouteCapabilitiesAreConsistent(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	planWrites := make([]string, 0)
	deniedReads := make([]string, 0)
	for _, capability := range RouteCapabilities() {
		key := capability.Method + " " + capability.Pattern
		if _, ok := seen[key]; ok {
			t.Errorf("route capability %q is declared more than once", key)
		}
		seen[key] = struct{}{}

		if strings.HasPrefix(capability.Pattern, "/api/daemon/") {
			t.Errorf("route capability %q names a daemon route, which task tokens never reach", key)
		}
		if capability.PlanWriteAllowed {
			planWrites = append(planWrites, capability.Pattern)
			if capability.Method != http.MethodPost {
				t.Errorf("plan write capability %q must be a POST route", key)
			}
			if !PlanRouteAllowed(capability.Method, capability.Pattern) {
				t.Errorf("declared plan write capability %q is not allowed by PlanRouteAllowed", key)
			}
			if IsRequirementWriteRoute(capability.Method, capability.Pattern) && capability.Pattern != RequirementCandidatesRoute {
				t.Errorf("plan write capability %q mutates requirement state outside the candidate channel", key)
			}
		}
		if capability.PlanReadDenied {
			deniedReads = append(deniedReads, capability.Pattern)
			switch capability.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				t.Errorf("plan read denial %q must be a read route", key)
			}
			if PlanRouteAllowed(capability.Method, capability.Pattern) {
				t.Errorf("declared plan read denial %q is allowed by PlanRouteAllowed", key)
			}
		}
		if capability.ExecuteRequirementWriteAllowed {
			if !IsRequirementWriteRoute(capability.Method, capability.Pattern) {
				t.Errorf("execute requirement write capability %q is not a requirement write", key)
			}
			if !RequirementTaskWriteRouteAllowed(capability.Method, capability.Pattern) {
				t.Errorf("declared execute capability %q is not allowed by RequirementTaskWriteRouteAllowed", key)
			}
		}
	}
	if !slices.Equal(PlanUnsafeRoutes(), sorted(planWrites)) {
		t.Errorf("plan unsafe routes are not derived from the declared capabilities")
	}
	if !slices.Equal(PlanDeniedReadRoutes(), sorted(deniedReads)) {
		t.Errorf("plan denied reads are not derived from the declared capabilities")
	}

	// The returned descriptors are copies too.
	capabilities := RouteCapabilities()
	capabilities[0].Pattern = "/api/issues"
	if RouteCapabilities()[0].Pattern == "/api/issues" {
		t.Fatal("RouteCapabilities exposed the internal declaration table")
	}
}

func sorted(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

func TestPlanLifecycleAllowlistUsesRouteSemantics(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	router.Post(RequirementCandidatesRoute, func(http.ResponseWriter, *http.Request) {})
	router.Post(IssueTaskCancelRoute, func(http.ResponseWriter, *http.Request) {})
	router.Post(TaskCancelRoute, func(http.ResponseWriter, *http.Request) {})
	router.Post("/api/issues", func(http.ResponseWriter, *http.Request) {})
	router.Get("/api/issues/{id}", func(http.ResponseWriter, *http.Request) {})
	router.Get(PluginSurfaceLaunchRoute, func(http.ResponseWriter, *http.Request) {})

	for _, tc := range []struct {
		path    string
		method  string
		allowed bool
	}{
		{"/api/workspaces/ws-1/requirements/req-1/candidates", http.MethodPost, true},
		{"/api/issues/issue-1/tasks/task-1/cancel", http.MethodPost, false},
		{"/api/tasks/task-1/cancel", http.MethodPost, false},
		{"/api/issues", http.MethodPost, false},
		{"/api/issues/issue-1", http.MethodGet, true},
		{"/api/workspaces/ws-1/plugins/install-1/surfaces/main/launch", http.MethodGet, false},
		{"/api/not-a-route", http.MethodPost, false},
	} {
		routeContext := chi.NewRouteContext()
		pattern := router.Find(routeContext, tc.method, tc.path)
		if got := PlanRouteAllowed(tc.method, pattern); got != tc.allowed {
			t.Errorf("route %q resolved to %q: allowed=%v, want %v", tc.path, pattern, got, tc.allowed)
		}
	}
}
