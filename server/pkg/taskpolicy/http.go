package taskpolicy

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

const (
	RequirementCandidatesRoute = "/api/workspaces/{id}/requirements/{requirementId}/candidates"
	RequirementBlockRoute      = "/api/workspaces/{id}/requirements/{requirementId}/block"

	// Cancellation uses the default write policy: denied for plan tasks,
	// allowed for execute tasks subject to the handler's access checks.
	IssueTaskCancelRoute = "/api/issues/{id}/tasks/{taskId}/cancel"
	TaskCancelRoute      = "/api/tasks/{taskId}/cancel"

	// PluginSurfaceLaunchRoute serves executable plugin bytes. It is a read by
	// method only, so it must be denied explicitly rather than through the
	// requirement write rule.
	PluginSurfaceLaunchRoute = "/api/workspaces/{id}/plugins/{installationId}/surfaces/{surfaceKey}/launch"
)

const requirementRoutePrefix = "/api/workspaces/{id}/requirements"

// RouteCapability declares one route whose task-token policy differs from its
// mode defaults:
//
//   - plan reads (GET, HEAD, OPTIONS) are allowed; PlanReadDenied closes one;
//   - plan writes are denied; PlanWriteAllowed opens one;
//   - execute writes are allowed unless they mutate requirement state, where
//     only ExecuteRequirementWriteAllowed opens one.
//
// The table is the source of truth for the predicates below and the router
// synchronization test. Only routes served under middleware.Auth belong here:
// /api/daemon is guarded by middleware.DaemonAuth, which rejects task tokens
// before any task policy runs, so a declaration for it would never take effect.
type RouteCapability struct {
	Method  string
	Pattern string

	PlanWriteAllowed bool
	PlanReadDenied   bool

	ExecuteRequirementWriteAllowed bool
}

// routeCapabilities is the explicit declaration of every route whose task-token
// policy differs from the mode defaults. Order is stable for review; the
// derived allowlists sort their output.
var routeCapabilities = []RouteCapability{
	{Method: http.MethodPost, Pattern: RequirementCandidatesRoute, PlanWriteAllowed: true},
	{Method: http.MethodPost, Pattern: RequirementBlockRoute, ExecuteRequirementWriteAllowed: true},
	{Method: http.MethodGet, Pattern: PluginSurfaceLaunchRoute, PlanReadDenied: true},
}

// RouteCapabilities returns a copy of the declared route capability table.
// Callers must not be able to widen policy by mutating the result.
func RouteCapabilities() []RouteCapability {
	return slices.Clone(routeCapabilities)
}

func routeCapability(method, routePattern string) (RouteCapability, bool) {
	for _, capability := range routeCapabilities {
		if capability.Method == method && capability.Pattern == routePattern {
			return capability, true
		}
	}
	return RouteCapability{}, false
}

// RequirementTaskWriteRouteAllowed reports whether an execute-mode task token
// may perform the given requirement write. Only the block capability is
// delegated to task-scoped agents.
func RequirementTaskWriteRouteAllowed(method, routePattern string) bool {
	if !IsRequirementWriteRoute(method, routePattern) {
		return false
	}
	capability, declared := routeCapability(method, routePattern)
	return declared && capability.ExecuteRequirementWriteAllowed
}

// IsRequirementWriteRoute reports whether the resolved route mutates
// requirement state. An empty route pattern is not a requirement write; callers
// must treat it as a policy-resolution failure before asking this question —
// see PlanRouteAllowed and ExecuteRouteAllowed.
func IsRequirementWriteRoute(method, routePattern string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return strings.HasPrefix(routePattern, requirementRoutePrefix)
}

// PlanUnsafeRoutes returns the write route patterns a plan-mode task token may
// call, sorted for stable output. Every entry must be a route the server
// actually registers; a stale entry silently widens nothing but hides drift.
func PlanUnsafeRoutes() []string {
	return declaredRoutePatterns(func(capability RouteCapability) bool {
		return capability.PlanWriteAllowed
	})
}

// PlanDeniedReadRoutes returns the read route patterns a plan-mode task token
// may not call, sorted for stable output. Every entry must be a route the
// server actually registers.
func PlanDeniedReadRoutes() []string {
	return declaredRoutePatterns(func(capability RouteCapability) bool {
		return capability.PlanReadDenied
	})
}

func declaredRoutePatterns(include func(RouteCapability) bool) []string {
	patterns := make([]string, 0, len(routeCapabilities))
	for _, capability := range routeCapabilities {
		if include(capability) {
			patterns = append(patterns, capability.Pattern)
		}
	}
	sort.Strings(patterns)
	return patterns
}

// PlanRouteAllowed reports whether a plan-mode task token may call the resolved
// route. A plan task is read-only by default: reads are allowed unless explicitly
// denied, and writes are allowed only from the explicit capability table. An
// unresolved route pattern (empty string) is a policy-resolution failure and
// fails closed.
func PlanRouteAllowed(method, routePattern string) bool {
	if routePattern == "" {
		return false
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// A read denial applies to the route, not just the method that served
		// the declaration: HEAD and OPTIONS must not become a way around a
		// denied GET.
		return !planReadDenied(routePattern)
	case http.MethodPost:
		capability, declared := routeCapability(method, routePattern)
		return declared && capability.PlanWriteAllowed
	default:
		return false
	}
}

func planReadDenied(routePattern string) bool {
	for _, capability := range routeCapabilities {
		if capability.PlanReadDenied && capability.Pattern == routePattern {
			return true
		}
	}
	return false
}

// ExecuteRouteAllowed reports whether an execute-mode task token may call the
// resolved route. Execute tasks are general workers: ordinary writes are
// allowed, but requirement writes are limited to the block capability.
//
// An unresolved route pattern (empty string) means the request did not match a
// registered route, so its policy cannot be determined. Fail closed instead of
// falling through as a "non-requirement write": a route added later behind a
// handler the router cannot introspect must be denied by default, not allowed
// by omission.
func ExecuteRouteAllowed(method, routePattern string) bool {
	if routePattern == "" {
		return false
	}
	if !IsRequirementWriteRoute(method, routePattern) {
		return true
	}
	return RequirementTaskWriteRouteAllowed(method, routePattern)
}
