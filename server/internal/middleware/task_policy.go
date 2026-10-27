package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func taskTokenModeScopeValid(token db.GetTaskTokenByHashRow, mode string) bool {
	switch mode {
	case taskpolicy.ModePlan:
		return token.RequirementID.Valid && token.ChatSessionID.Valid && !token.IssueID.Valid
	case taskpolicy.ModeExecute:
		return !token.RequirementID.Valid || (token.IssueID.Valid && !token.ChatSessionID.Valid)
	default:
		return false
	}
}

// taskRoutePattern resolves the Chi route pattern that will serve the request.
// An empty pattern means the path did not match a registered route: the caller
// must fail closed because the route's policy is unknown (a mount or handler
// introduced later may still reach application code).
func taskRoutePattern(r *http.Request, routes []chi.Routes) string {
	if len(routes) == 0 || routes[0] == nil {
		return ""
	}
	return routes[0].Find(chi.NewRouteContext(), r.Method, r.URL.Path)
}

func planTaskRequestAllowed(r *http.Request, routes []chi.Routes) bool {
	return taskpolicy.PlanRouteAllowed(r.Method, taskRoutePattern(r, routes))
}

func requirementTaskWriteRequestAllowed(r *http.Request, routes []chi.Routes) bool {
	return taskpolicy.ExecuteRouteAllowed(r.Method, taskRoutePattern(r, routes))
}
