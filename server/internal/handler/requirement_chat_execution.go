package handler

import (
	"errors"
	"net/http"

	"github.com/multica-ai/multica/server/internal/service"
)

func writeRequirementChatExecutionError(w http.ResponseWriter, err error) bool {
	var gateErr *service.RequirementChatExecutionError
	if !errors.As(err, &gateErr) {
		return false
	}
	body := map[string]any{
		"error": gateErr.Error(),
		"code":  gateErr.Code,
	}
	if gateErr.IssueID != "" {
		body["issue_id"] = gateErr.IssueID
	}
	writeJSON(w, http.StatusConflict, body)
	return true
}
