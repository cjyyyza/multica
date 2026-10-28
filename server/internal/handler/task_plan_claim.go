package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func (h *Handler) validateTaskModeForClaim(r *http.Request, task *db.AgentTaskQueue, runtime db.AgentRuntime) (string, *claimBuildFailure) {
	mode, err := taskpolicy.NormalizeMode(task.Mode)
	if err != nil {
		return "", h.failClaimedTaskBeforeLaunch(r.Context(), task,
			"This task has an unsupported execution mode.",
			taskfailure.ReasonInvalidTaskIdentity, "invalid_task_mode", http.StatusConflict, "unsupported task mode")
	}
	if err := h.TaskService.ValidateTaskRequirement(r.Context(), *task, runtime.WorkspaceID); err != nil {
		var gateErr *service.RequirementChatExecutionError
		permanent := errors.As(err, &gateErr) || errors.Is(err, service.ErrTaskRequirementBinding) || errors.Is(err, pgx.ErrNoRows)
		if !permanent {
			return "", &claimBuildFailure{outcome: "error_requirement_validation", status: http.StatusInternalServerError, message: "failed to validate task requirement"}
		}
		message := "This task's requirement binding is no longer valid."
		if gateErr != nil {
			message = gateErr.Error()
		}
		return "", h.failClaimedTaskBeforeLaunch(r.Context(), task, message,
			taskfailure.ReasonInvalidTaskIdentity, "invalid_task_requirement", http.StatusConflict, "task requirement is not claimable")
	}
	if mode != taskpolicy.ModePlan {
		return mode, nil
	}
	if !requestHasClientCapability(r, protocol.DaemonCapabilityPlanModeV1) || !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilityPlanModeV1) {
		return "", h.failClaimedTaskBeforeLaunch(r.Context(), task,
			"Plan Mode requires a daemon with plan-mode-v1 support. Update the daemon; this task was not executed.",
			taskfailure.ReasonInvalidTaskIdentity, "plan_daemon_unsupported", http.StatusConflict, "daemon does not support Plan Mode")
	}
	if runtime.ProfileID.Valid || !agent.SupportsPlanMode(runtime.Provider) {
		return "", h.failClaimedTaskBeforeLaunch(r.Context(), task,
			fmt.Sprintf("Plan Mode is not available for runtime provider %q. Use a built-in Claude or DSH runtime; this task was not executed.", runtime.Provider),
			taskfailure.ReasonInvalidTaskIdentity, "plan_provider_unsupported", http.StatusConflict, "runtime provider does not support Plan Mode")
	}
	return mode, nil
}
