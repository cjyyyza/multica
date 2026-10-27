package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func noopHandler(http.ResponseWriter, *http.Request) {}

// productionRoutes builds the router shape the server really assembles: the
// request enters the Auth group, which registers nested Route groups and a
// member group below a mount. Route patterns must survive that nesting because
// the middleware resolves them from the same router instance.
func productionRoutes(t *testing.T) chi.Routes {
	t.Helper()
	root := chi.NewRouter()
	var routes chi.Routes
	root.Group(func(r chi.Router) {
		routes = r
		r.Route("/api/workspaces", func(r chi.Router) {
			r.Route("/{id}", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Get("/requirements/{requirementId}", noopHandler)
					r.Post("/requirements/{requirementId}/block", noopHandler)
					r.Post("/requirements/{requirementId}/accept", noopHandler)
					r.Post("/requirements/{requirementId}/candidates", noopHandler)
				})
			})
		})
		r.Post("/api/issues/{id}/comments", noopHandler)
	})
	if routes == nil {
		t.Fatal("router assembly did not expose its Auth group")
	}
	return routes
}

func TestRequirementTaskWriteRequestAllowedUsesNestedRouterPatterns(t *testing.T) {
	t.Parallel()
	routes := productionRoutes(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"block is the execute requirement capability", http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/block", true},
		{"accept is not a task capability", http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/accept", false},
		{"candidates stay with plan tasks", http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/candidates", false},
		{"requirement reads are not writes", http.MethodGet, "/api/workspaces/ws-1/requirements/req-1", true},
		{"ordinary issue write stays available", http.MethodPost, "/api/issues/issue-1/comments", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if got := requirementTaskWriteRequestAllowed(req, []chi.Routes{routes}); got != tc.want {
				t.Fatalf("%s %q allowed=%v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestRequirementTaskWriteRequestFailsClosedWithoutResolvedRoute(t *testing.T) {
	t.Parallel()
	routes := productionRoutes(t)
	for _, tc := range []struct {
		name   string
		routes []chi.Routes
		method string
		path   string
	}{
		{"no router supplied", nil, http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/block"},
		{"nil router supplied", []chi.Routes{nil}, http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/block"},
		{"unknown nested path", []chi.Routes{routes}, http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/not-a-route"},
		{"unknown root path", []chi.Routes{routes}, http.MethodPost, "/api/not-a-route"},
		{"unknown nested read", []chi.Routes{routes}, http.MethodGet, "/api/workspaces/ws-1/requirements/req-1/not-a-route"},
		{"unknown root read", []chi.Routes{routes}, http.MethodGet, "/api/not-a-route"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if requirementTaskWriteRequestAllowed(req, tc.routes) {
				t.Fatalf("%s %q was allowed without a resolved route pattern", tc.method, tc.path)
			}
		})
	}
}

func TestNewRequirementWriteRouteIsDeniedUntilDeclared(t *testing.T) {
	t.Parallel()
	root := chi.NewRouter()
	var routes chi.Routes
	root.Group(func(r chi.Router) {
		routes = r
		r.Route("/api/workspaces", func(r chi.Router) {
			r.Route("/{id}", func(r chi.Router) {
				r.Post("/requirements/{requirementId}/block", noopHandler)
				// A future requirement write channel. Neither policy declares it,
				// so both modes must deny it rather than infer "not block =>
				// allowed" or "not requirement => allowed".
				r.Post("/requirements/{requirementId}/archive", noopHandler)
			})
		})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/archive", nil)
	if requirementTaskWriteRequestAllowed(req, []chi.Routes{routes}) {
		t.Fatal("execute task token was allowed to call an undeclared requirement write route")
	}
	if planTaskRequestAllowed(req, []chi.Routes{routes}) {
		t.Fatal("plan task token was allowed to call an undeclared requirement write route")
	}
}

func TestPlanTaskRequestAllowedUsesNestedRouterPatterns(t *testing.T) {
	t.Parallel()
	root := chi.NewRouter()
	var routes chi.Routes
	root.Group(func(r chi.Router) {
		routes = r
		r.Post("/api/workspaces/{id}/requirements/{requirementId}/candidates", noopHandler)
		r.Post("/api/issues/{id}/tasks/{taskId}/cancel", noopHandler)
		r.Post("/api/tasks/{taskId}/cancel", noopHandler)
		r.Post("/api/issues", noopHandler)
	})
	for _, tc := range []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/workspaces/ws-1/requirements/req-1/candidates", true},
		{http.MethodPost, "/api/issues/issue-1/tasks/task-1/cancel", false},
		{http.MethodPost, "/api/tasks/task-1/cancel", false},
		{http.MethodPost, "/api/issues", false},
		{http.MethodPost, "/api/not-a-route", false},
		{http.MethodGet, "/api/not-a-route", false},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := planTaskRequestAllowed(req, []chi.Routes{routes}); got != tc.want {
			t.Errorf("%s %q allowed=%v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestPlanTaskRequestFailsClosedWithoutRouter(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/issues", nil)
	if planTaskRequestAllowed(req, nil) {
		t.Fatal("plan task token read was allowed without a resolved route pattern")
	}
}

func TestPlanTaskCannotCancelTasks(t *testing.T) {
	t.Parallel()
	routes := chi.NewRouter()
	for _, pattern := range []string{"/api/issues/{id}/tasks/{taskId}/cancel", "/api/tasks/{taskId}/cancel"} {
		routes.Post(pattern, noopHandler)
	}
	for _, path := range []string{"/api/issues/issue-2/tasks/other-task/cancel", "/api/tasks/other-task/cancel", "/api/tasks/own-task/cancel"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-Task-ID", "own-task")
		if planTaskRequestAllowed(req, []chi.Routes{routes}) {
			t.Errorf("plan token can cancel a task through %s", path)
		}
		if !requirementTaskWriteRequestAllowed(req, []chi.Routes{routes}) {
			t.Errorf("execute task cancellation unexpectedly changed for %s", path)
		}
	}
}
