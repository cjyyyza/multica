package handler

import (
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func allowTaskExternalTools(w http.ResponseWriter, task db.AgentTaskQueue) bool {
	if taskpolicy.AllowsExternalTools(task.Mode) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "external tools are not available in Plan Mode",
		"code":  taskpolicy.PlanModeToolDenied,
	})
	return false
}
